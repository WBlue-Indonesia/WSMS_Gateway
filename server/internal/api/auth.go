package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
)

// clientAuth authenticates a client API key and enforces a required scope.
// Token format: "wsms_<prefix>.<secret>". Only the Argon2id hash of <secret> is stored.
func (s *Server) clientAuth(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		prefix, sec, ok := parseBearer(c.GetHeader("Authorization"))
		if !ok {
			abort(c, http.StatusUnauthorized, "missing or malformed bearer token")
			return
		}
		var key models.APIKey
		if err := s.db.Where("prefix = ? AND active = true", prefix).First(&key).Error; err != nil {
			abort(c, http.StatusUnauthorized, "invalid key")
			return
		}
		if !secret.Verify(sec, key.Hash) {
			abort(c, http.StatusUnauthorized, "invalid key")
			return
		}
		if !hasScope(key.Scopes, scope) {
			abort(c, http.StatusForbidden, "missing scope: "+scope)
			return
		}
		now := time.Now()
		s.db.Model(&models.APIKey{}).Where("id = ?", key.ID).Update("last_used_at", now)
		c.Set("client_id", key.ClientID)
		c.Next()
	}
}

func parseBearer(h string) (prefix, secretPart string, ok bool) {
	const p = "Bearer "
	if !strings.HasPrefix(h, p) {
		return "", "", false
	}
	tok := strings.TrimPrefix(h, p)
	tok = strings.TrimPrefix(tok, "wsms_")
	dot := strings.IndexByte(tok, '.')
	if dot <= 0 || dot == len(tok)-1 {
		return "", "", false
	}
	return tok[:dot], tok[dot+1:], true
}

func hasScope(csv, want string) bool {
	for _, s := range strings.Split(csv, ",") {
		s = strings.TrimSpace(s)
		if s == want || s == "admin" {
			return true
		}
	}
	return false
}

func abort(c *gin.Context, code int, msg string) {
	c.AbortWithStatusJSON(code, gin.H{"error": msg})
}
