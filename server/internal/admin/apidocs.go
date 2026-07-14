package admin

import (
	"fmt"
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

// ---- API reference model (docs/07 §7) --------------------------------------

// Param documents one request field for the fields table.
type Param struct {
	Name     string
	Type     string
	Required string // "required" | "optional" | a default like "default: ON_NET_PREF"
	Desc     string
}

// Endpoint is one documented route, rendered as a self-contained card.
type Endpoint struct {
	Method string
	Path   string
	Scope  string // "" → shown as a dash (no scope / not API-key auth)
	Desc   string
	Req    string   // request body sample (pretty JSON) or ""
	Resp   string   // response sample (pretty JSON) or ""
	Params []Param  // request fields (only on the main endpoints)
	Notes  []string // extra caveats, one per line
}

// Group buckets related endpoints under a heading.
type Group struct {
	Name string
	Desc string
	Slug string
	Rows []Endpoint
}

var apiGroups = []Group{
	{
		Name: "Messages", Slug: "messages",
		Desc: "Submit and track SMS. The server normalizes the number, detects the operator, segments the text, and routes on-net (same operator) with random fallback.",
		Rows: []Endpoint{
			{
				Method: "POST", Path: "/v1/messages", Scope: "messages:write",
				Desc: "Queue an SMS for delivery. Returns 202 once accepted; poll the message or use a webhook for the terminal status.",
				Params: []Param{
					{"to", "string", "required", "Indonesian mobile number in any common form (0812…, 62812…, +62812…). Normalized to +62."},
					{"message", "string", "required", "Message body. Encoding (GSM-7 / UCS-2) and segment count are detected automatically."},
					{"ttl_seconds", "int", "optional", "Time-to-live. A message with no life left is expired, never sent (protects stale OTPs)."},
					{"dedup_key", "string", "optional", "Idempotency key, unique per client. Re-submitting the same key + body replays the first result."},
					{"routing_policy", "string", "default: ON_NET_PREF", "ON_NET_PREF (prefer same operator, allow fallback) · ON_NET_STRICT (same operator only) · ANY (load-balanced)."},
					{"callback_url", "string", "optional", "HTTPS URL to receive an HMAC-signed webhook when the message reaches a terminal state."},
				},
				Req: `{
  "to": "0812-3456-7890",
  "message": "Kode OTP 123456 berlaku 5 menit.",
  "ttl_seconds": 300,
  "dedup_key": "otp-8842",
  "routing_policy": "ON_NET_PREF"
}`,
				Resp: `202 Accepted
{
  "id": "018f7a2b-1c3d-42de-bc3a-b5cd10943801",
  "status": "QUEUED",
  "target_operator": "TELKOMSEL",
  "encoding": "GSM7",
  "segments": 1,
  "expires_at": "2026-07-15T00:05:00Z"
}`,
				Notes: []string{
					"Idempotent replay of a known dedup_key returns 200 with \"idempotent_replay\": true.",
					"Reusing a dedup_key with a different body returns 409.",
					"Subject to a per-client rate limit — a burst over the limit returns 429 with Retry-After.",
				},
			},
			{
				Method: "GET", Path: "/v1/messages/:id", Scope: "messages:read",
				Desc: "Fetch a single message you submitted. Add ?include=events for the full lifecycle timeline.",
				Resp: `200 OK
{
  "message": {
    "id": "018f7a2b-1c3d-42de-bc3a-b5cd10943801",
    "status": "DELIVERED",
    "target_msisdn": "+6281234567890",
    "target_operator": "TELKOMSEL",
    "assigned_operator": "TELKOMSEL",
    "encoding": "GSM7", "segments": 1,
    "attempts": 1, "max_attempts": 3,
    "created_at": "2026-07-15T00:00:00Z",
    "expires_at": "2026-07-15T00:05:00Z"
  }
}`,
				Notes: []string{"With ?include=events, an \"events\" array of {event_type, created_at} is added."},
			},
			{
				Method: "GET", Path: "/v1/messages", Scope: "messages:read",
				Desc:  "List your recent messages (newest first, up to 50).",
				Notes: []string{"Filters: ?status=DELIVERED · ?operator=TELKOMSEL"},
				Resp: `200 OK
{ "messages": [ { "id": "…", "status": "SENT_UNCONFIRMED", … } ], "count": 50 }`,
			},
			{
				Method: "POST", Path: "/v1/messages/:id/cancel", Scope: "messages:write",
				Desc: "Attempt to cancel a message. Only a message that has not left the radio can truly be cancelled.",
				Resp: `200 OK   { "id": "…", "status": "CANCELLED" }          (was still QUEUED)
202 Accepted { "id": "…", "status": "cancel_requested" }   (in flight — best effort)
409 Conflict { "error": "message already SENT" }           (terminal — cannot cancel)`,
			},
		},
	},
	{
		Name: "Fleet", Slug: "fleet",
		Desc: "Read-only visibility into the phone fleet and its SIMs. Useful for ON_NET_STRICT clients to pre-check on-net capacity before submitting.",
		Rows: []Endpoint{
			{
				Method: "GET", Path: "/v1/devices", Scope: "devices:read",
				Desc: "List devices with their live online state.",
				Resp: `200 OK
{
  "devices": [ { "device": { "id": "…", "name": "HP-A Gudang", "status": "ONLINE" }, "online": true } ],
  "online_count": 2
}`,
			},
			{
				Method: "GET", Path: "/v1/sims", Scope: "sims:read",
				Desc: "List SIMs across the fleet. Add ?on_net_ready=true for per-operator counts of READY SIMs on online devices.",
				Resp: `200 OK  (?on_net_ready=true)
{
  "sims": [ { "id": "…", "operator": "TELKOMSEL", "status": "READY", "daily_quota": 500, "sent_today": 214 } ],
  "on_net_ready": { "TELKOMSEL": 2, "INDOSAT": 1 }
}`,
			},
		},
	},
	{
		Name: "Device", Slug: "device",
		Desc: "Endpoints used by the Android sender app itself. Not for API clients — the WebSocket protocol is documented in docs/02.",
		Rows: []Endpoint{
			{
				Method: "POST", Path: "/v1/device/enroll", Scope: "",
				Desc: "Pair a phone: exchange a single-use enrollment token (issued from Enrollment) for a device_id + device_secret.",
				Req: `{
  "token": "<enrollment-token>",
  "name": "HP-A",
  "os": "android",
  "sims": [ { "subscription_id": 1, "slot": 0, "carrier_name": "Telkomsel" } ]
}`,
				Resp: `200 OK
{ "device_id": "018f…", "device_secret": "…" }   ← shown once; the app stores it securely`,
			},
			{
				Method: "GET", Path: "/v1/device/ws", Scope: "",
				Desc:  "Device WebSocket (persistent). Carries send commands down and acks / delivery reports / sim state up.",
				Notes: []string{"Auth header: Authorization: Bearer dev_<device_id>.<device_secret>"},
			},
		},
	},
	{
		Name: "Health", Slug: "health",
		Desc: "Unauthenticated operational probes.",
		Rows: []Endpoint{
			{Method: "GET", Path: "/healthz", Scope: "", Desc: "Liveness — the process is up and the database answers.", Resp: `200 OK  { "ok": true }`},
			{
				Method: "GET", Path: "/readyz", Scope: "",
				Desc: "Readiness — DB reachable AND ≥1 device online AND ≥1 SIM ready. Coarse; it does not guarantee capacity for a specific operator.",
				Resp: `200 OK   { "ready": true }
503      { "ready": false, "reasons": ["no_device_online"] }`,
			},
		},
	},
}

// httpCode documents an error status in the status-code table.
type httpCode struct {
	Code string
	Kind string // ok | warn | bad
	Desc string
}

var apiHTTPCodes = []httpCode{
	{"200 / 202", "ok", "Success. 202 Accepted means the message was queued (async delivery)."},
	{"400", "bad", "Bad request — malformed body, invalid number, or unknown routing_policy."},
	{"401", "bad", "Missing/invalid bearer token, bad request signature, or a disabled client."},
	{"403", "bad", "The key is valid but lacks the required scope."},
	{"404", "bad", "No such message (or not owned by your client)."},
	{"409", "warn", "Conflict — dedup_key reused with a different body, or cancelling a terminal message."},
	{"429", "warn", "Rate limit exceeded. Honor the Retry-After header and back off."},
	{"500 / 503", "bad", "Server error / not ready. Retry with backoff."},
}

// lifecycleStep is one node in the message state machine diagram.
type lifecycleStep struct {
	Name string
	Kind string // muted | warn | ok | bad
	Desc string
}

var apiLifecycle = []lifecycleStep{
	{"QUEUED", "muted", "Accepted, waiting for a SIM."},
	{"ROUTING", "muted", "A worker is selecting a SIM."},
	{"DISPATCHED", "muted", "Command sent to a device."},
	{"AWAITING_ACK", "warn", "On the wire, ack not yet seen — never re-routed."},
	{"SENT", "ok", "Left the radio; awaiting a delivery report."},
	{"DELIVERED", "ok", "Confirmed delivered."},
	{"SENT_UNCONFIRMED", "warn", "Sent, but no report arrived (normal on many IDN routes)."},
	{"FAILED", "bad", "Terminal failure."},
	{"EXPIRED", "bad", "TTL elapsed before it could be sent."},
	{"CANCELLED", "bad", "Cancelled before send."},
}

func (s *Server) apiDocsPage(c *gin.Context) {
	renderPage(c, "apidocs", gin.H{
		"Groups":    apiGroups,
		"Codes":     apiHTTPCodes,
		"Lifecycle": apiLifecycle,
		"BaseURL":   s.baseURL(c),
		"RateLimit": fmt.Sprintf("%.0f req/s per client (burst %d)", s.cfg.RatePerSec, s.cfg.RateBurst),
	})
}

func (s *Server) openAPISpec(c *gin.Context) {
	b, err := staticFS.ReadFile("static/openapi.json")
	if err != nil {
		c.String(http.StatusInternalServerError, "spec unavailable")
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", b)
}
