package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/nizwar/wsms-gateway/server/internal/fleet"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
)

// Devices connect over the private network; origin is not a meaningful check here.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(*http.Request) bool { return true },
}

type enrollReq struct {
	Token      string        `json:"token" binding:"required"`
	Name       string        `json:"name" binding:"required"`
	AppVersion string        `json:"app_version"`
	OS         string        `json:"os"`
	Model      string        `json:"model"`
	Sims       []ws.SimInfo  `json:"sims"`
}

// enrollDevice exchanges a single-use enrollment token for a device_id + device_secret.
func (s *Server) enrollDevice(c *gin.Context) {
	var req enrollReq
	if err := c.ShouldBindJSON(&req); err != nil {
		abort(c, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	var tok models.EnrollmentToken
	err := s.db.Where("token_hash = ? AND used = false AND expires_at > now()", secret.SHA256Hex(req.Token)).First(&tok).Error
	if err != nil {
		abort(c, http.StatusUnauthorized, "invalid or expired enrollment token")
		return
	}

	deviceSecret, _ := secret.RandomToken(32)
	hash, _ := secret.Hash(deviceSecret)
	now := time.Now()
	device := models.Device{
		ID: models.NewID(), Name: req.Name, Status: models.DevEnrolled,
		AppVersion: req.AppVersion, SecretHash: hash, LastSeenAt: &now,
	}
	if err := s.db.Create(&device).Error; err != nil {
		abort(c, http.StatusInternalServerError, "could not create device")
		return
	}
	s.db.Model(&models.EnrollmentToken{}).Where("id = ?", tok.ID).
		Updates(map[string]any{"used": true, "used_at": now, "device_id": device.ID})

	if len(req.Sims) > 0 {
		_ = fleet.UpsertSims(s.db, device.ID, req.Sims)
	}

	c.JSON(http.StatusOK, gin.H{
		"device_id":     device.ID,
		"device_secret": deviceSecret, // shown ONCE — the app stores it securely
	})
}

// deviceWS upgrades an authenticated device connection.
// Auth: Authorization: Bearer dev_<device_id>.<device_secret>.
func (s *Server) deviceWS(c *gin.Context) {
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
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // upgrade writes its own error
	}
	ws.Serve(s.hub, conn, device.ID) // blocks until the socket closes
}

func parseDeviceBearer(h string) (deviceID, secretPart string, ok bool) {
	const p = "Bearer dev_"
	if len(h) <= len(p) || h[:len(p)] != p {
		return "", "", false
	}
	tok := h[len(p):]
	for i := 0; i < len(tok); i++ {
		if tok[i] == '.' {
			if i == 0 || i == len(tok)-1 {
				return "", "", false
			}
			return tok[:i], tok[i+1:], true
		}
	}
	return "", "", false
}
