// Package api wires the HTTP/REST surface and the device WebSocket endpoint.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/config"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
	"gorm.io/gorm"
)

type Server struct {
	db     *gorm.DB
	hub    *ws.Hub
	engine *router.Engine
	cfg    config.Config
}

func New(db *gorm.DB, hub *ws.Hub, engine *router.Engine, cfg config.Config) *Server {
	return &Server{db: db, hub: hub, engine: engine, cfg: cfg}
}

// Handler builds the gin engine with all routes.
func (s *Server) Handler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", s.healthz)
	r.GET("/readyz", s.readyz)

	v1 := r.Group("/v1")
	{
		// Client-facing (API-key auth).
		v1.POST("/messages", s.clientAuth("messages:write"), s.submitMessage)
		v1.GET("/messages/:id", s.clientAuth("messages:read"), s.getMessage)
		v1.GET("/messages", s.clientAuth("messages:read"), s.listMessages)
		v1.POST("/messages/:id/cancel", s.clientAuth("messages:write"), s.cancelMessage)
		v1.GET("/devices", s.clientAuth("devices:read"), s.listDevices)
		v1.GET("/sims", s.clientAuth("sims:read"), s.listSims)

		// Device-facing.
		v1.POST("/device/enroll", s.enrollDevice)
		v1.GET("/device/ws", s.deviceWS)
	}
	return r
}

func (s *Server) healthz(c *gin.Context) {
	sqlDB, err := s.db.DB()
	if err == nil {
		err = sqlDB.Ping()
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "db": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// readyz: coarse go/no-go. Note (amendment F14): green here does NOT guarantee capacity
// for a specific operator — ON_NET_STRICT clients must check GET /v1/sims for on-net readiness.
func (s *Server) readyz(c *gin.Context) {
	reasons := []string{}
	sqlDB, err := s.db.DB()
	if err != nil || sqlDB.Ping() != nil {
		reasons = append(reasons, "db_unreachable")
	}
	if s.hub.OnlineCount() == 0 {
		reasons = append(reasons, "no_device_online")
	}
	var readySims int64
	s.db.Model(&models.Sim{}).Where("status = ?", models.SimReady).Count(&readySims)
	if readySims == 0 {
		reasons = append(reasons, "no_sim_ready")
	}
	if len(reasons) > 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"ready": false, "reasons": reasons})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ready": true})
}
