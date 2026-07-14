package admin

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
)

// mutate roles: only owner/operator may change fleet/message state.
func (s *Server) canMutate(c *gin.Context) bool {
	r := s.role(c)
	return r == "owner" || r == "operator"
}

func (s *Server) simDisable(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	s.db.Model(&models.Sim{}).Where("id = ?", c.Param("id")).
		Updates(map[string]any{"status": models.SimDisabled, "updated_at": time.Now()})
	s.audit(c, "sim.disable", "sim", c.Param("id"), "")
	c.Redirect(http.StatusSeeOther, "/admin/fleet")
}

func (s *Server) simEnable(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	s.db.Model(&models.Sim{}).Where("id = ?", c.Param("id")).
		Updates(map[string]any{"status": models.SimReady, "cooldown_until": nil, "updated_at": time.Now()})
	s.audit(c, "sim.enable", "sim", c.Param("id"), "")
	c.Redirect(http.StatusSeeOther, "/admin/fleet")
}

func (s *Server) simCooldown(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	until := time.Now().Add(30 * time.Minute)
	s.db.Model(&models.Sim{}).Where("id = ?", c.Param("id")).
		Updates(map[string]any{"status": models.SimCooldown, "cooldown_until": until, "updated_at": time.Now()})
	s.audit(c, "sim.cooldown", "sim", c.Param("id"), "30m")
	c.Redirect(http.StatusSeeOther, "/admin/fleet")
}

// deviceRescan asks a connected device to re-report its SIMs.
func (s *Server) deviceRescan(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	f, _ := ws.Encode(ws.TypeConfig, models.NewID(), time.Now().UnixMilli(), map[string]any{"action": "report_sims"})
	_ = s.hub.SendTo(c.Param("id"), f)
	s.audit(c, "device.rescan", "device", c.Param("id"), "")
	c.Redirect(http.StatusSeeOther, "/admin/fleet")
}

// messageCancel cancels a message and re-renders the detail drawer (htmx).
func (s *Server) messageCancel(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	id := c.Param("id")
	var msg models.Message
	if s.db.First(&msg, "id = ?", id).Error != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	// Pre-dispatch: cancel outright only if still QUEUED (never sent).
	res := s.db.Model(&models.Message{}).Where("id = ? AND status = ?", id, models.MsgQueued).
		Updates(map[string]any{"status": models.MsgCancelled, "updated_at": time.Now(), "last_error": "cancelled by operator"})
	if res.RowsAffected == 0 {
		switch msg.Status {
		case models.MsgSent, models.MsgSentUnconfirmed, models.MsgDelivered, models.MsgFailed, models.MsgExpired, models.MsgCancelled:
			// terminal — leave as-is (F9: don't lie about a sent message)
		default:
			// in flight — best-effort cancel request to the device
			if msg.AssignedDeviceID != nil {
				f, _ := ws.Encode(ws.TypeCancel, models.NewID(), time.Now().UnixMilli(), ws.CancelData{MessageID: id})
				_ = s.hub.SendTo(*msg.AssignedDeviceID, f)
			}
		}
	}
	s.audit(c, "message.cancel", "message", id, "")
	// re-render the drawer with fresh state
	s.messageDetail(c)
}
