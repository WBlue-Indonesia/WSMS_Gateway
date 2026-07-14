package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
	"github.com/nizwar/wsms-gateway/server/internal/smstext"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
	"gorm.io/gorm"
)

type submitReq struct {
	To            string `json:"to" binding:"required"`
	Message       string `json:"message" binding:"required"`
	DedupKey      string `json:"dedup_key"`
	TTLSeconds    int    `json:"ttl_seconds"`
	RoutingPolicy string `json:"routing_policy"`
	CallbackURL   string `json:"callback_url"`
}

func (s *Server) submitMessage(c *gin.Context) {
	clientID := c.GetString("client_id")
	var req submitReq
	if err := c.ShouldBindJSON(&req); err != nil {
		abort(c, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	canonical, ok := router.NormalizeMSISDN(req.To)
	if !ok {
		abort(c, http.StatusBadRequest, "invalid Indonesian MSISDN: "+req.To)
		return
	}
	policy := models.RoutingPolicy(req.RoutingPolicy)
	switch policy {
	case models.PolicyOnNetPref, models.PolicyOnNetStrict, models.PolicyAny:
	case "":
		policy = models.PolicyOnNetPref
	default:
		abort(c, http.StatusBadRequest, "invalid routing_policy")
		return
	}

	enc, segs := smstext.Analyze(req.Message)
	op := s.engine.Detect(canonical)
	payloadHash := secret.SHA256Hex(clientID + "|" + canonical + "|" + req.Message)

	// Idempotency (F12): same dedup_key + same payload → replay; different payload → 409.
	if req.DedupKey != "" {
		var existing models.Message
		err := s.db.Where("client_id = ? AND dedup_key = ?", clientID, req.DedupKey).First(&existing).Error
		if err == nil {
			if existing.PayloadHash == payloadHash {
				c.JSON(http.StatusOK, gin.H{"id": existing.ID, "status": existing.Status, "idempotent_replay": true})
			} else {
				abort(c, http.StatusConflict, "dedup_key reused with a different payload")
			}
			return
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			abort(c, http.StatusInternalServerError, "lookup failed")
			return
		}
	}

	ttl := s.cfg.DefaultTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	var dedup *string
	if req.DedupKey != "" {
		dedup = &req.DedupKey
	}
	msg := models.Message{
		ID:             models.NewID(),
		ClientID:       clientID,
		TargetMSISDN:   canonical,
		TargetOperator: op,
		Body:           req.Message,
		Encoding:       models.Encoding(enc),
		Segments:       segs,
		Status:         models.MsgQueued,
		RoutingPolicy:  policy,
		DedupKey:       dedup,
		PayloadHash:    payloadHash,
		MaxAttempts:    3,
		CallbackURL:    req.CallbackURL,
		ExpiresAt:      time.Now().Add(ttl),
	}
	if err := s.db.Create(&msg).Error; err != nil {
		// Unique-index race on (client_id, dedup_key): fetch and replay.
		if dedup != nil {
			var existing models.Message
			if s.db.Where("client_id = ? AND dedup_key = ?", clientID, req.DedupKey).First(&existing).Error == nil {
				c.JSON(http.StatusOK, gin.H{"id": existing.ID, "status": existing.Status, "idempotent_replay": true})
				return
			}
		}
		abort(c, http.StatusInternalServerError, "could not queue message")
		return
	}
	s.db.Create(&models.MessageEvent{ID: models.NewID(), MessageID: msg.ID, EventType: models.EvSubmitted, CreatedAt: time.Now()})

	c.JSON(http.StatusAccepted, gin.H{
		"id": msg.ID, "status": msg.Status, "target_operator": op,
		"encoding": enc, "segments": segs, "expires_at": msg.ExpiresAt,
	})
}

func (s *Server) getMessage(c *gin.Context) {
	clientID := c.GetString("client_id")
	var msg models.Message
	if s.db.Where("id = ? AND client_id = ?", c.Param("id"), clientID).First(&msg).Error != nil {
		abort(c, http.StatusNotFound, "not found")
		return
	}
	resp := gin.H{"message": msg}
	if c.Query("include") == "events" {
		var events []models.MessageEvent
		s.db.Where("message_id = ?", msg.ID).Order("created_at").Find(&events)
		resp["events"] = events
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) listMessages(c *gin.Context) {
	clientID := c.GetString("client_id")
	q := s.db.Where("client_id = ?", clientID)
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	if op := c.Query("operator"); op != "" {
		q = q.Where("target_operator = ?", op)
	}
	limit := 50
	var msgs []models.Message
	q.Order("created_at DESC").Limit(limit).Find(&msgs)
	c.JSON(http.StatusOK, gin.H{"messages": msgs, "count": len(msgs)})
}

// cancelMessage: safe cancel semantics (amendment F9). QUEUED can be cancelled outright;
// an in-flight (DISPATCHED) message returns 202 cancel_requested and is only truly
// cancelled if the device confirms it dropped the send before SmsManager.
func (s *Server) cancelMessage(c *gin.Context) {
	clientID := c.GetString("client_id")
	id := c.Param("id")
	var msg models.Message
	if s.db.Where("id = ? AND client_id = ?", id, clientID).First(&msg).Error != nil {
		abort(c, http.StatusNotFound, "not found")
		return
	}
	// Pre-dispatch: atomically cancel only if still QUEUED (never sent).
	res := s.db.Model(&models.Message{}).
		Where("id = ? AND status = ?", id, models.MsgQueued).
		Updates(map[string]any{"status": models.MsgCancelled, "updated_at": time.Now(), "last_error": "cancelled by client"})
	if res.RowsAffected > 0 {
		c.JSON(http.StatusOK, gin.H{"id": id, "status": models.MsgCancelled})
		return
	}
	switch msg.Status {
	case models.MsgSent, models.MsgSentUnconfirmed, models.MsgDelivered, models.MsgFailed, models.MsgExpired, models.MsgCancelled:
		abort(c, http.StatusConflict, "message already "+string(msg.Status))
	default:
		// In flight — request a best-effort cancel from the assigned device.
		if msg.AssignedDeviceID != nil {
			f, _ := ws.Encode(ws.TypeCancel, models.NewID(), time.Now().UnixMilli(), ws.CancelData{MessageID: id})
			_ = s.hub.SendTo(*msg.AssignedDeviceID, f)
		}
		c.JSON(http.StatusAccepted, gin.H{"id": id, "status": "cancel_requested"})
	}
}
