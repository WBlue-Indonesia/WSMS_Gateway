package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/fleet"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
)

// deviceAuth authenticates a device by its bearer token (dev_<device_id>.<device_secret>)
// and sets device_id on the context. Used by the push-driven REST endpoints: the device,
// woken by FCM, sends the SMS and confirms the result here — no persistent socket.
func (s *Server) deviceAuth(c *gin.Context) {
	deviceID, deviceSecret, ok := parseDeviceBearer(c.GetHeader("Authorization"))
	if !ok {
		abort(c, http.StatusUnauthorized, "missing device token")
		return
	}
	var device models.Device
	if s.db.Where("id = ? AND status <> ?", deviceID, models.DevDisabled).First(&device).Error != nil {
		abort(c, http.StatusUnauthorized, "unknown device")
		return
	}
	if !secret.Verify(deviceSecret, device.SecretHash) {
		abort(c, http.StatusUnauthorized, "bad device secret")
		return
	}
	c.Set("device_id", deviceID)
	c.Next()
}

// registerToken stores/refreshes the device's FCM token (the delivery address).
func (s *Server) registerToken(c *gin.Context) {
	var req struct {
		PushToken string `json:"push_token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		abort(c, http.StatusBadRequest, "invalid body")
		return
	}
	now := time.Now()
	s.db.Model(&models.Device{}).Where("id = ?", c.GetString("device_id")).
		Updates(map[string]any{"push_token": req.PushToken, "last_seen_at": now, "wake_misses": 0, "updated_at": now})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// reportSimsREST upserts the device's SIM set and returns the authoritative sim state.
func (s *Server) reportSimsREST(c *gin.Context) {
	deviceID := c.GetString("device_id")
	var req ws.SimReportData
	if err := c.ShouldBindJSON(&req); err != nil {
		abort(c, http.StatusBadRequest, "invalid body")
		return
	}
	if err := fleet.UpsertSims(s.db, deviceID, req.Sims); err != nil {
		abort(c, http.StatusInternalServerError, "sim upsert failed")
		return
	}
	s.touchDevice(deviceID)
	states, _ := fleet.DeviceSimStates(s.db, deviceID)
	c.JSON(http.StatusOK, gin.H{"sims": states})
}

// deviceSimsREST returns the current per-SIM state (operator/status/quota/sent).
func (s *Server) deviceSimsREST(c *gin.Context) {
	states, _ := fleet.DeviceSimStates(s.db, c.GetString("device_id"))
	c.JSON(http.StatusOK, gin.H{"sims": states})
}

// deviceAckREST is the REST equivalent of the WS send_ack frame.
func (s *Server) deviceAckREST(c *gin.Context) {
	var a ws.SendAckData
	if err := c.ShouldBindJSON(&a); err != nil || a.MessageID == "" {
		abort(c, http.StatusBadRequest, "invalid body")
		return
	}
	if !s.ownsMessage(c.GetString("device_id"), a.MessageID) {
		abort(c, http.StatusForbidden, "message not assigned to this device")
		return
	}
	s.touchDevice(c.GetString("device_id"))
	if s.disp != nil {
		s.disp.HandleDeviceAck(c.Request.Context(), a)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// deviceDeliveryREST is the REST equivalent of the WS delivery_report frame.
func (s *Server) deviceDeliveryREST(c *gin.Context) {
	var dr ws.DeliveryReportData
	if err := c.ShouldBindJSON(&dr); err != nil || dr.MessageID == "" {
		abort(c, http.StatusBadRequest, "invalid body")
		return
	}
	if !s.ownsMessage(c.GetString("device_id"), dr.MessageID) {
		abort(c, http.StatusForbidden, "message not assigned to this device")
		return
	}
	if s.disp != nil {
		s.disp.HandleDeviceDelivery(c.Request.Context(), dr)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// deviceSetQuotaREST lets the device adjust a SIM's daily quota (clamped, audited).
func (s *Server) deviceSetQuotaREST(c *gin.Context) {
	deviceID := c.GetString("device_id")
	var req ws.SetQuotaData
	if err := c.ShouldBindJSON(&req); err != nil {
		abort(c, http.StatusBadRequest, "invalid body")
		return
	}
	if simID, clamped, ok := fleet.SetSimQuota(s.db, deviceID, req.SubscriptionID, req.DailyQuota); ok {
		s.db.Create(&models.AdminAudit{
			ID: models.NewID(), Actor: "device:" + deviceID, ActorRole: "device",
			Action: "sim.quota", TargetType: "sim", TargetID: simID,
			Reason: strconv.Itoa(clamped), CreatedAt: time.Now(),
		})
	}
	states, _ := fleet.DeviceSimStates(s.db, deviceID)
	c.JSON(http.StatusOK, gin.H{"sims": states})
}

// ownsMessage guards ack/delivery: a device may only report on a message assigned to it.
func (s *Server) ownsMessage(deviceID, messageID string) bool {
	var msg models.Message
	if s.db.Select("assigned_device_id").First(&msg, "id = ?", messageID).Error != nil {
		return false
	}
	return msg.AssignedDeviceID != nil && *msg.AssignedDeviceID == deviceID
}

func (s *Server) touchDevice(deviceID string) {
	now := time.Now()
	s.db.Model(&models.Device{}).Where("id = ?", deviceID).
		Updates(map[string]any{"last_seen_at": now, "wake_misses": 0, "updated_at": now})
}
