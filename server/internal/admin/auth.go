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
	s.mu.Lock()
	s.sessions[tok] = session{userID: u.ID, username: u.Username, role: u.Role, expires: time.Now().Add(12 * time.Hour)}
	s.mu.Unlock()

	now := time.Now()
	s.db.Model(&models.AdminUser{}).Where("id = ?", u.ID).Update("last_login_at", now)

	secure := c.Request.TLS != nil
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(cookieName, tok, int((12 * time.Hour).Seconds()), "/admin", "", secure, true)
	c.Redirect(http.StatusSeeOther, "/admin")
}

func (s *Server) doLogout(c *gin.Context) {
	if tok, err := c.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, tok)
		s.mu.Unlock()
	}
	c.SetCookie(cookieName, "", -1, "/admin", "", false, true)
	c.Redirect(http.StatusSeeOther, "/admin/login")
}
