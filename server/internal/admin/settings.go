package admin

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/i18n"
	"github.com/nizwar/wsms-gateway/server/internal/models"
)

// setLang persists the chosen UI language in a cookie (Path=/ so it covers both the
// console and the public "/" page) and redirects back to a local next path.
func (s *Server) setLang(c *gin.Context) {
	code := c.Param("code")
	if !i18n.IsSupported(code) {
		code = i18n.Default
	}
	secure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(i18n.CookieName, code, 365*24*3600, "/", "", secure, false)
	next := c.Query("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/admin"
	}
	c.Redirect(http.StatusSeeOther, next)
}

// settingsFragment renders the Settings modal body: language switch + routing policy.
func (s *Server) settingsFragment(c *gin.Context) {
	s.renderSettings(c, false)
}

// renderSettings renders the settings modal fragment. saved marks a just-persisted
// routing change so the modal can show a confirmation.
func (s *Server) renderSettings(c *gin.Context, saved bool) {
	fbMode, fbOp := s.engine.Fallback()
	// Only operators we actually own a READY SIM for are offered as a default.
	var owned []models.Operator
	s.db.Model(&models.Sim{}).
		Where("deleted_at IS NULL AND status = ?", models.SimReady).
		Distinct().Order("operator").Pluck("operator", &owned)
	renderFragment(c, "settings", gin.H{
		"CanMutate": s.canMutate(c),
		"Saved":     saved,
		"Routing": gin.H{
			"Mode":     string(fbMode),
			"Operator": string(fbOp),
			"Owned":    owned,
		},
	})
}
