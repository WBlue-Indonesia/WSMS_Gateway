// Package admin is the server-rendered operator console (docs/07). It mounts under
// /admin in the same Go binary, reuses the GORM store and the ws.Hub, and serves
// html/template + vendored htmx — no SPA, no external assets.
package admin

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/config"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
	"gorm.io/gorm"
)

const cookieName = "wsms_admin"

// sessionTTL is how long a persisted admin session stays valid.
const sessionTTL = 30 * 24 * time.Hour

type Server struct {
	db     *gorm.DB
	hub    *ws.Hub
	cfg    config.Config
	engine *router.Engine

	flashMu sync.Mutex
	flashes map[string]flashItem
}

func New(db *gorm.DB, hub *ws.Hub, cfg config.Config, engine *router.Engine) *Server {
	buildTemplates()
	return &Server{db: db, hub: hub, cfg: cfg, engine: engine, flashes: map[string]flashItem{}}
}

// Mount wires the admin routes onto the given gin engine and bootstraps the owner user.
func (s *Server) Mount(r *gin.Engine) {
	s.bootstrapOwner()

	g := r.Group("/admin")
	g.StaticFS("/static", http.FS(mustSub()))
	g.GET("/manifest.webmanifest", s.serveManifest) // PWA (public)
	g.GET("/sw.js", s.serviceWorker)                // PWA service worker (public, scope /admin/)
	g.GET("/login", s.loginPage)
	g.POST("/login", s.doLogin)
	g.POST("/logout", s.doLogout)

	a := g.Group("")
	a.Use(s.requireSession)
	a.GET("", s.overview)
	a.GET("/", s.overview)
	a.GET("/messages", s.messagesPage)
	a.GET("/compose", s.composePage)
	a.POST("/compose", s.sendCompose)
	a.GET("/messages/rows", s.messagesRows)
	a.GET("/messages/:id", s.messageDetail)
	a.POST("/messages/:id/unmask", s.unmaskMSISDN)
	a.POST("/messages/:id/cancel", s.messageCancel)
	a.GET("/fleet", s.fleet)
	a.POST("/sims/:id/disable", s.simDisable)
	a.POST("/sims/:id/enable", s.simEnable)
	a.POST("/sims/:id/cooldown", s.simCooldown)
	a.POST("/sims/:id/quota", s.simQuota)
	a.POST("/devices/:id/rescan", s.deviceRescan)
	a.POST("/devices/:id/rename", s.deviceRename)
	a.POST("/devices/:id/delete", s.deviceDelete)
	a.GET("/enrollment", s.enrollmentPage)
	a.POST("/enrollment", s.issueEnrollment)
	a.POST("/enrollment/:id/delete", s.deleteEnrollment)
	a.GET("/clients", s.clientsPage)
	a.POST("/clients", s.createClient)
	a.POST("/clients/:id/toggle", s.toggleClient)
	a.POST("/clients/:id/delete", s.deleteClient)
	a.POST("/clients/:id/keys", s.createKey)
	a.POST("/clients/:id/webhook-secret", s.rotateWebhookSecret)
	a.POST("/keys/:id/revoke", s.revokeKey)
	a.POST("/keys/:id/enable-signing", s.enableKeySigning)
	a.GET("/apidocs", s.apiDocsPage)
	a.GET("/openapi.json", s.openAPISpec)
}

// bootstrapOwner creates an initial owner admin user if none exist, logging the
// one-time password.
func (s *Server) bootstrapOwner() {
	var n int64
	if s.db.Model(&models.AdminUser{}).Count(&n); n > 0 {
		return
	}
	pw, _ := secret.RandomToken(12)
	hash, _ := secret.Hash(pw)
	u := models.AdminUser{ID: models.NewID(), Username: "admin", PasswordHash: hash, Role: "owner", Active: true}
	if err := s.db.Create(&u).Error; err != nil {
		slog.Error("admin bootstrap failed", "err", err)
		return
	}
	slog.Warn("BOOTSTRAP admin console login (store it now)", "username", "admin", "password", pw)
}

// requireSession gates authed admin routes.
func (s *Server) requireSession(c *gin.Context) {
	tok, err := c.Cookie(cookieName)
	if err == nil {
		var sess models.AdminSession
		if s.db.Where("token_hash = ? AND expires_at > now()", secret.SHA256Hex(tok)).First(&sess).Error == nil {
			c.Set("admin_user", gin.H{"username": sess.Username, "role": sess.Role, "id": sess.UserID})
			c.Set("role", sess.Role)
			c.Next()
			return
		}
	}
	if c.GetHeader("HX-Request") == "true" {
		c.Header("HX-Redirect", "/admin/login")
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	c.Redirect(http.StatusSeeOther, "/admin/login")
	c.Abort()
}

// role helpers (docs/07 §3 RBAC).
func (s *Server) role(c *gin.Context) string { return c.GetString("role") }

func canUnmask(role string) bool { return role == "owner" || role == "operator" || role == "support" }

func (s *Server) audit(c *gin.Context, action, targetType, targetID, reason string) {
	u, _ := c.Get("admin_user")
	m, _ := u.(gin.H)
	actor, role := "", ""
	if m != nil {
		actor, _ = m["username"].(string)
		role, _ = m["role"].(string)
	}
	s.db.Create(&models.AdminAudit{
		ID: models.NewID(), Actor: actor, ActorRole: role, Action: action,
		TargetType: targetType, TargetID: targetID, Reason: reason,
		SourceIP: c.ClientIP(), CreatedAt: time.Now(),
	})
}
