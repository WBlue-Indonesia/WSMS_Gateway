// Package api wires the HTTP/REST surface and the device WebSocket endpoint.
package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/admin"
	"github.com/nizwar/wsms-gateway/server/internal/config"
	"github.com/nizwar/wsms-gateway/server/internal/metrics"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
)

// DeliveryReporter is the dispatcher's device-result surface, so the REST device
// endpoints (push-driven ack/delivery) reuse the exact WS lifecycle logic.
type DeliveryReporter interface {
	HandleDeviceAck(ctx context.Context, a ws.SendAckData)
	HandleDeviceDelivery(ctx context.Context, dr ws.DeliveryReportData)
}

type Server struct {
	db     *gorm.DB
	hub    *ws.Hub
	engine *router.Engine
	cfg    config.Config
	admin  *admin.Server
	disp   DeliveryReporter
	rl     *rateLimiter
}

func New(db *gorm.DB, hub *ws.Hub, engine *router.Engine, cfg config.Config, disp DeliveryReporter) *Server {
	return &Server{
		db: db, hub: hub, engine: engine, cfg: cfg,
		admin: admin.New(db, hub, cfg, engine),
		disp:  disp,
		rl:    newRateLimiter(cfg.RatePerSec, cfg.RateBurst),
	}
}

// Handler builds the gin engine with all routes.
func (s *Server) Handler() http.Handler {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/healthz", s.healthz)
	r.GET("/readyz", s.readyz)

	// Prometheus metrics (internal; scrape target — docs/06 §3.4).
	reg := prometheus.NewRegistry()
	reg.MustRegister(metrics.New(s.db, s.hub))
	r.GET("/metrics", gin.WrapH(promhttp.HandlerFor(reg, promhttp.HandlerOpts{})))

	// Admin console (server-rendered, mounted in this same binary under /admin).
	if s.admin != nil {
		// Public landing page at "/" — unauthenticated, explains the system (sms.wblue.id).
		r.GET("/", s.admin.PublicHome)
		s.admin.Mount(r)
	}

	v1 := r.Group("/v1")
	{
		// Client-facing (API-key auth).
		v1.POST("/messages", s.clientAuth("messages:write"), s.rl.middleware(), s.submitMessage)
		v1.GET("/messages/:id", s.clientAuth("messages:read"), s.getMessage)
		v1.GET("/messages", s.clientAuth("messages:read"), s.listMessages)
		v1.POST("/messages/:id/cancel", s.clientAuth("messages:write"), s.cancelMessage)
		v1.GET("/devices", s.clientAuth("devices:read"), s.listDevices)
		v1.GET("/sims", s.clientAuth("sims:read"), s.listSims)

		// Device-facing.
		v1.POST("/device/enroll", s.enrollDevice) // no device token yet
		v1.GET("/device/ws", s.deviceWS)          // WS (legacy transport; own auth)

		// Push-driven device endpoints (device bearer auth). The device is woken by an
		// FCM data message carrying the send command, sends the SMS, and confirms here.
		dev := v1.Group("/device")
		dev.Use(s.deviceAuth)
		dev.POST("/token", s.registerToken)
		dev.POST("/report-sims", s.reportSimsREST)
		dev.GET("/sims", s.deviceSimsREST)
		dev.POST("/ack", s.deviceAckREST)
		dev.POST("/delivery", s.deviceDeliveryREST)
		dev.POST("/set-quota", s.deviceSetQuotaREST)
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
