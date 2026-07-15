package admin

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
)

func (s *Server) loginPage(c *gin.Context) {
	renderPage(c, "login", gin.H{"Error": c.Query("error")})
}

func (s *Server) doLogin(c *gin.Context) {
	username := c.PostForm("username")
	password := c.PostForm("password")
	var u models.AdminUser
	if s.db.Where("username = ? AND active = true", username).First(&u).Error != nil ||
		!secret.Verify(password, u.PasswordHash) {
		c.Redirect(http.StatusSeeOther, "/admin/login?error=invalid+credentials")
		return
	}
	tok, _ := secret.RandomToken(24)
	now := time.Now()
	// Persist the session in the DB so it survives restarts/redeploys and the same user
	// can be signed in from any number of devices at once (each login is its own row).
	s.db.Create(&models.AdminSession{
		TokenHash: secret.SHA256Hex(tok), UserID: u.ID, Username: u.Username, Role: u.Role,
		SourceIP: c.ClientIP(), UserAgent: c.GetHeader("User-Agent"),
		ExpiresAt: now.Add(sessionTTL), CreatedAt: now,
	})
	s.db.Model(&models.AdminUser{}).Where("id = ?", u.ID).Update("last_login_at", now)

	// Behind a TLS-terminating proxy (Cloudflare/nginx) the Go server sees plain HTTP,
	// so also honor X-Forwarded-Proto. Lax lets the cookie ride top-level navigations.
	secure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cookieName, tok, int(sessionTTL.Seconds()), "/admin", "", secure, true)
	c.Redirect(http.StatusSeeOther, "/admin")
}

func (s *Server) doLogout(c *gin.Context) {
	if tok, err := c.Cookie(cookieName); err == nil {
		s.db.Delete(&models.AdminSession{}, "token_hash = ?", secret.SHA256Hex(tok))
	}
	c.SetCookie(cookieName, "", -1, "/admin", "", false, true)
	c.Redirect(http.StatusSeeOther, "/admin/login")
}
