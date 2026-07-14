package admin

import (
	"encoding/base64"
	"encoding/json"
	"html/template"

	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"
)

// baseURL returns the externally-reachable server URL used in the pairing QR:
// the configured WSMS_PUBLIC_URL, else derived from the admin request.
func (s *Server) baseURL(c *gin.Context) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

// pairPayload is the JSON the app reads from the QR: server URL + one-time token.
type pairPayload struct {
	V     int    `json:"v"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

// pairQR returns (payload JSON string, inline PNG data-URI) for a pairing QR.
func pairQR(baseURL, token string) (string, template.URL) {
	payload, _ := json.Marshal(pairPayload{V: 1, URL: baseURL, Token: token})
	png, err := qrcode.Encode(string(payload), qrcode.Medium, 320)
	if err != nil {
		return string(payload), ""
	}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	return string(payload), template.URL(uri)
}
