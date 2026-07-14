package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
)

func newEnrollToken() (plain, hash string) {
	plain, _ = secret.RandomToken(24)
	return plain, secret.SHA256Hex(plain)
}

func secretToken() (string, error) { return secret.RandomToken(32) }

func sealSecret(key []byte, plain string) ([]byte, error) {
	return secret.Seal(key, []byte(plain))
}

// Endpoint is one row in the human-readable API reference.
type Endpoint struct {
	Method string
	Path   string
	Scope  string
	Desc   string
	Body   string
}

var apiEndpoints = []Endpoint{
	{"POST", "/v1/messages", "messages:write", "Submit an SMS. Server detects operator + segments and queues it.",
		`{"to":"0812xxxx","message":"...","ttl_seconds":300,"dedup_key":"otp-1","routing_policy":"ON_NET_PREF"}`},
	{"GET", "/v1/messages/:id", "messages:read", "Fetch one message; add ?include=events for the lifecycle timeline.", ""},
	{"GET", "/v1/messages", "messages:read", "List recent messages. Filters: ?status= &operator=", ""},
	{"POST", "/v1/messages/:id/cancel", "messages:write", "Cancel: 200 if still queued, 202 cancel_requested if in flight, 409 if terminal.", ""},
	{"GET", "/v1/devices", "devices:read", "List devices + live online state.", ""},
	{"GET", "/v1/sims", "sims:read", "List SIMs. ?on_net_ready=true adds per-operator READY counts.", ""},
	{"POST", "/v1/device/enroll", "—", "Device pairing: exchange an enrollment token for device_id + device_secret.",
		`{"token":"...","name":"HP-A","os":"android","sims":[{"subscription_id":1,"slot":0,"carrier_name":"Telkomsel"}]}`},
	{"GET", "/v1/device/ws", "—", "Device WebSocket. Auth: Bearer dev_<device_id>.<device_secret>.", ""},
	{"GET", "/healthz", "—", "Liveness (DB ping).", ""},
	{"GET", "/readyz", "—", "Readiness: DB up + >=1 device online + >=1 SIM ready (coarse, see F14).", ""},
}

func (s *Server) apiDocsPage(c *gin.Context) {
	renderPage(c, "apidocs", gin.H{"Endpoints": apiEndpoints})
}

func (s *Server) openAPISpec(c *gin.Context) {
	b, err := staticFS.ReadFile("static/openapi.json")
	if err != nil {
		c.String(http.StatusInternalServerError, "spec unavailable")
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}
