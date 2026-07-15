package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/i18n"
)

// PublicHome serves the unauthenticated landing page mounted at "/" (e.g.
// https://sms.wblue.id/). It explains what the system is and who it is for — a
// visitor who is not an operator or a registered client should understand at a
// glance that this is a private gateway, not a public SMS service. It is registered
// by the api layer OUTSIDE the /admin auth group, so it must never leak state.
func (s *Server) PublicHome(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusOK)
	if err := publicTmpl.Execute(c.Writer, gin.H{"AssetVer": assetVer, "Lang": i18n.Resolve(c.Request)}); err != nil {
		c.String(http.StatusInternalServerError, "render error")
	}
}

// serveManifest serves the PWA manifest (kept off /admin/static so its URL is stable).
func (s *Server) serveManifest(c *gin.Context) {
	b, err := staticFS.ReadFile("static/manifest.webmanifest")
	if err != nil {
		c.String(http.StatusInternalServerError, "unavailable")
		return
	}
	c.Data(http.StatusOK, "application/manifest+json; charset=utf-8", b)
}

// serviceWorker serves the PWA service worker from /admin/sw.js so its default scope
// covers the whole /admin/ console (a worker under /admin/static/ could not).
func (s *Server) serviceWorker(c *gin.Context) {
	b, err := staticFS.ReadFile("static/sw.js")
	if err != nil {
		c.String(http.StatusInternalServerError, "unavailable")
		return
	}
	c.Header("Service-Worker-Allowed", "/admin")
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/javascript; charset=utf-8", b)
}
