package admin

import (
	"net/http"
	"strconv"
	"strings"
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

// deviceRename updates a device's display name.
func (s *Server) deviceRename(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.Redirect(http.StatusSeeOther, "/admin/fleet")
		return
	}
	s.db.Model(&models.Device{}).Where("id = ?", c.Param("id")).
		Updates(map[string]any{"name": name, "updated_at": time.Now()})
	s.audit(c, "device.rename", "device", c.Param("id"), name)
	c.Redirect(http.StatusSeeOther, "/admin/fleet")
}

// deviceDelete unlinks a device from the console (Fleet). See unlinkDevice.
func (s *Server) deviceDelete(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	s.unlinkDevice(c.Param("id"))
	s.audit(c, "device.delete", "device", c.Param("id"), "")
	c.Redirect(http.StatusSeeOther, "/admin/fleet")
}

// unlinkDevice fully detaches a phone: it force-closes the live WebSocket so the
// phone drops immediately, then soft-deletes the device, its SIMs, and any enrollment
// token that paired it. A soft-deleted device row is invisible to deviceWS auth
// (which does a plain First), so the phone can no longer reconnect — it is unlinked.
func (s *Server) unlinkDevice(deviceID string) {
	s.hub.Disconnect(deviceID) // kill the live socket now, not at the next ping timeout
	s.db.Where("device_id = ?", deviceID).Delete(&models.Sim{})
	s.db.Where("device_id = ?", deviceID).Delete(&models.EnrollmentToken{})
	s.db.Delete(&models.Device{}, "id = ?", deviceID)
}

// simQuota sets a SIM's daily segment quota (segments/day).
func (s *Server) simQuota(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	q, err := strconv.Atoi(strings.TrimSpace(c.PostForm("daily_quota")))
	if err != nil || q < 0 {
		c.Redirect(http.StatusSeeOther, "/admin/fleet")
		return
	}
	if q > 100000 {
		q = 100000 // sanity ceiling
	}
	s.db.Model(&models.Sim{}).Where("id = ?", c.Param("id")).
		Updates(map[string]any{"daily_quota": q, "updated_at": time.Now()})
	s.audit(c, "sim.quota", "sim", c.Param("id"), strconv.Itoa(q))
	c.Redirect(http.StatusSeeOther, "/admin/fleet")
}

// saveRouting persists the global routing fallback policy (default-SIM behavior) and
// applies it to the live dispatcher. Governs how a SIM is chosen for off-net numbers —
// those whose operator you don't own a SIM for, or when the on-net SIM is out of quota.
func (s *Server) saveRouting(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	mode := models.FallbackMode(strings.TrimSpace(c.PostForm("mode")))
	op := models.Operator(strings.TrimSpace(c.PostForm("operator")))
	switch mode {
	case models.FallbackLeastLoaded, models.FallbackRandom:
		op = models.OpUnknown // operator only meaningful for DEFAULT_OP
	case models.FallbackDefaultOp:
		if !validOperator(op) {
			c.Redirect(http.StatusSeeOther, "/admin/fleet")
			return
		}
	default:
		c.Redirect(http.StatusSeeOther, "/admin/fleet")
		return
	}
	s.db.Model(&models.AppSettings{}).Where("id = ?", models.SettingsID).
		Updates(map[string]any{"fallback_mode": mode, "fallback_operator": op, "updated_at": time.Now()})
	s.engine.SetFallback(mode, op)
	s.audit(c, "routing.fallback", "settings", "1", string(mode)+" "+string(op))
	c.Redirect(http.StatusSeeOther, "/admin/fleet")
}

// validOperator reports whether op is a real (non-UNKNOWN) mobile operator.
func validOperator(op models.Operator) bool {
	switch op {
	case models.OpTelkomsel, models.OpIndosat, models.OpXL, models.OpAxis, models.OpTri, models.OpSmartfren:
		return true
	}
	return false
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
