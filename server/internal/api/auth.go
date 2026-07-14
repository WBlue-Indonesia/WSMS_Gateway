package api

import (
	"bytes"
	"io"
	"math"
	"net/http"
	"strconv"
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
		if !s.verifySigning(c, key) {
			abort(c, http.StatusUnauthorized, "invalid request signature")
			return
		}
		now := time.Now()
		s.db.Model(&models.APIKey{}).Where("id = ?", key.ID).Update("last_used_at", now)
		c.Set("client_id", key.ClientID)
		c.Next()
	}
}

// verifySigning enforces inbound HMAC request signing when the key has a signing
// secret configured (amendment F3). The signing secret is stored AES-GCM-encrypted
// (SigningSecretEnc) — distinct from the one-way Argon2id bearer hash, which cannot
// be used to recompute an HMAC. Signature = HMAC-SHA256(signingSecret, ts + "." + body).
func (s *Server) verifySigning(c *gin.Context, key models.APIKey) bool {
	if len(key.SigningSecretEnc) == 0 {
		return true // signing not enabled for this key
	}
	sigHdr := c.GetHeader("X-WSMS-Signature")
	tsHdr := c.GetHeader("X-WSMS-Timestamp")
	if sigHdr == "" || tsHdr == "" {
		return false
	}
	ts, err := strconv.ParseInt(tsHdr, 10, 64)
	if err != nil || math.Abs(float64(time.Now().Unix()-ts)) > 300 {
		return false // stale/invalid timestamp (±5 min window)
	}
	body, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewReader(body)) // restore for the handler
	sk, err := secret.Open(s.cfg.MasterKey[:], key.SigningSecretEnc)
	if err != nil {
		return false
	}
	want := secret.HMACSHA256Hex(sk, []byte(tsHdr+"."+string(body)))
	got := strings.TrimPrefix(sigHdr, "sha256=")
	return secret.EqualHex(want, got)
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
