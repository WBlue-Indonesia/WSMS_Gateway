package admin

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/i18n"
)

// assetVer is a content hash of the CSS, appended as ?v= to asset URLs so a proxy
// or browser can never serve a stale stylesheet after a redeploy.
var assetVer = "0"

func mustSub() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

//go:embed templates/*.html
var tmplFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Full pages are wrapped in layout.html; fragments (htmx partials) render bare.
var fullPages = []string{"login", "overview", "messages", "compose", "fleet", "enrollment", "clients", "apidocs"}
var fragments = []string{"message_detail", "messages_rows", "unmask", "settings", "fleet_detail"}

var (
	pageTmpls  map[string]*template.Template
	fragTmpls  map[string]*template.Template
	publicTmpl *template.Template // standalone marketing/info page at "/" (no layout, no auth)
)

var funcs = template.FuncMap{
	"maskMSISDN": maskMSISDN,
	"shortID": func(s string) string {
		if len(s) > 8 {
			return s[:8]
		}
		return s
	},
	"fmtTime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Local().Format("2006-01-02 15:04:05")
	},
	"fmtTimeP": func(t *time.Time) string {
		if t == nil || t.IsZero() {
			return "—"
		}
		return t.Local().Format("2006-01-02 15:04")
	},
	"fmtShort": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Local().Format("01-02 15:04")
	},
	"since": since,
	"badge": statusBadge,
	"pct": func(n, d int64) string {
		if d == 0 {
			return "—"
		}
		return fmt.Sprintf("%.0f%%", float64(n)*100/float64(d))
	},
	"addI": func(a, b int64) int64 { return a + b },
	"list": func(xs ...string) []string { return xs },
	"quotaPct": func(sent, quota int) int {
		if quota <= 0 {
			return 0
		}
		p := sent * 100 / quota
		if p > 100 {
			return 100
		}
		return p
	},
	"lower":      strings.ToLower,
	"sparkline":  sparkline,
	"statusIcon": statusIcon,
	// t localizes key for the given language (id→en→key fallback).
	"t": i18n.T,
	// scopeField maps a scope like "messages:write" to its form field name
	// "scope_messages_write" (matches createKey's parser).
	"scopeField": func(s string) string { return "scope_" + strings.ReplaceAll(s, ":", "_") },
	// ratio returns n/max as a 0..100 int for bar widths (min 2% when n>0, so a
	// non-zero value is always visible). Args may be int or int64.
	"ratio": func(n, max any) int {
		nn, mm := toI64(n), toI64(max)
		if mm <= 0 {
			return 0
		}
		r := int(nn * 100 / mm)
		if r > 100 {
			r = 100
		}
		if r < 2 && nn > 0 {
			r = 2
		}
		return r
	},
}

// toI64 coerces the numeric types the templates hand to ratio into int64.
func toI64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func buildTemplates() {
	if b, err := staticFS.ReadFile("static/admin.css"); err == nil {
		sum := sha256.Sum256(b)
		assetVer = hex.EncodeToString(sum[:])[:10]
	}
	pageTmpls = map[string]*template.Template{}
	for _, p := range fullPages {
		t := template.New("layout.html").Funcs(funcs)
		t = template.Must(t.ParseFS(tmplFS, "templates/layout.html", "templates/partials.html", "templates/"+p+".html"))
		pageTmpls[p] = t
	}
	fragTmpls = map[string]*template.Template{}
	for _, f := range fragments {
		t := template.New(f).Funcs(funcs)
		t = template.Must(t.ParseFS(tmplFS, "templates/partials.html", "templates/"+f+".html"))
		fragTmpls[f] = t
	}
	publicTmpl = template.Must(template.New("public.html").Funcs(funcs).ParseFS(tmplFS, "templates/public.html"))
}

func renderPage(c *gin.Context, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["Page"] = name
	data["AssetVer"] = assetVer
	data["Lang"] = i18n.Resolve(c.Request)
	if u, ok := c.Get("admin_user"); ok {
		data["User"] = u
	}
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpls[name].ExecuteTemplate(c.Writer, "layout.html", data); err != nil {
		c.String(http.StatusInternalServerError, "render error: %v", err)
	}
}

func renderFragment(c *gin.Context, name string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	if _, ok := data["Lang"]; !ok {
		data["Lang"] = i18n.Resolve(c.Request)
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := fragTmpls[name].ExecuteTemplate(c.Writer, name, data); err != nil {
		c.String(http.StatusInternalServerError, "render error: %v", err)
	}
}

// maskMSISDN renders +6281****7890 — first 5 and last 2 digits visible.
func maskMSISDN(s string) string {
	if len(s) <= 7 {
		return s
	}
	return s[:5] + strings.Repeat("*", len(s)-7) + s[len(s)-2:]
}

func since(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// statusIcon renders a compact, colored inline SVG for a message status (with a title
// tooltip) — used in the message log instead of a text pill.
func statusIcon(status string) template.HTML {
	kind := statusBadge(status)
	var d string
	switch status {
	case "DELIVERED":
		d = `<path d="M1.5 12.5 6 17 14 8"/><path d="m12 16 2.5 2.5L23 8"/>` // double check
	case "SENT":
		d = `<path d="M5 13l4 4L19 7"/>` // check
	case "SENT_UNCONFIRMED":
		d = `<path d="M5 13l4 4L19 7" stroke-dasharray="3 2.5"/>` // dashed check
	case "FAILED", "EXPIRED":
		d = `<path d="M18 6 6 18M6 6l12 12"/>` // x
	case "CANCELLED":
		d = `<circle cx="12" cy="12" r="9"/><path d="M8 12h8"/>` // no-entry
	default: // QUEUED, ROUTING, DISPATCHED, AWAITING_ACK
		d = `<circle cx="12" cy="12" r="9"/><path d="M12 7.5V12l3 2"/>` // clock (in flight)
	}
	return template.HTML(fmt.Sprintf(
		`<span class="sicon %s" title="%s"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">%s</svg></span>`,
		kind, status, d))
}

func statusBadge(status string) string {
	switch status {
	case "DELIVERED", "SENT", "READY", "ONLINE":
		return "ok"
	case "FAILED", "EXPIRED", "DISABLED", "ABSENT":
		return "bad"
	case "SENT_UNCONFIRMED", "COOLDOWN", "QUOTA_EXCEEDED", "AWAITING_ACK":
		return "warn"
	default:
		return "muted"
	}
}

// sparkline renders a tiny inline SVG polyline (no JS) from a slice of ints.
func sparkline(vals []int) template.HTML {
	if len(vals) == 0 {
		return ""
	}
	const w, h = 120, 28
	mx := 1
	for _, v := range vals {
		if v > mx {
			mx = v
		}
	}
	denom := len(vals) - 1
	if denom < 1 {
		denom = 1
	}
	step := float64(w) / float64(denom)
	var pts strings.Builder
	for i, v := range vals {
		x := float64(i) * step
		y := float64(h) - (float64(v)/float64(mx))*float64(h-2) - 1
		fmt.Fprintf(&pts, "%.1f,%.1f ", x, y)
	}
	return template.HTML(fmt.Sprintf(
		`<svg aria-hidden="true" focusable="false" width="%d" height="%d" viewBox="0 0 %d %d" class="spark"><polyline points="%s"/></svg>`,
		w, h, w, h, strings.TrimSpace(pts.String())))
}
