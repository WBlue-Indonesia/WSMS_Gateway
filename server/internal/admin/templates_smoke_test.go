package admin

import (
	"bytes"
	"testing"

	"github.com/nizwar/wsms-gateway/server/internal/models"
)

type tkv struct {
	K string
	N int64
}

func TestTemplatesParseAndRender(t *testing.T) {
	buildTemplates() // template.Must panics here if any page fails to parse

	pages := map[string]map[string]any{
		"login": {"Error": ""},
		"overview": {"OnlineDevices": 1, "TotalDevices": int64(2), "ReadySims": int64(1), "TotalSims": int64(2),
			"QueueDepth": int64(0), "Delivered": int64(3), "Failed": int64(1), "Total24": int64(4),
			"SuccessNum": int64(3), "SuccessDen": int64(4), "OnNet": int64(2), "Fallback": int64(1),
			"SegToday": int64(9), "QuotaSent": int64(9), "QuotaTotal": int64(100), "Series": []int{1, 2, 0, 3, 4, 1, 2},
			"StatusMap": map[string]int64{"DELIVERED": 3}, "StatusBars": []tkv{{K: "DELIVERED", N: 3}},
			"Operators": []tkv{{K: "XL", N: 2}}, "OpMax": int64(2), "OpReady": []tkv{{K: "XL", N: 1}},
			"OpReadyMax": int64(1), "Attention": int64(0)},
		"clients":    {"Clients": nil, "Scopes": knownScopes, "CanMutate": true},
		"fleet":      {"Devices": nil, "CanMutate": true},
		"enrollment": {"Tokens": nil, "CanMutate": true},
		"compose":    {"Error": "", "Sent": ""},
		"messages":   {"Messages": nil, "Q": "", "Status": "", "Operator": ""},
		"apidocs":    {"Groups": apiGroups, "Codes": apiHTTPCodes, "Lifecycle": apiLifecycle, "BaseURL": "http://localhost:8080", "RateLimit": "5 req/s per client (burst 10)"},
	}
	for _, lang := range []string{"id", "en"} {
		for name, data := range pages {
			data["Page"] = name
			data["AssetVer"] = "test"
			data["Lang"] = lang
			data["User"] = map[string]any{"username": "admin", "role": "owner"}
			var buf bytes.Buffer
			if err := pageTmpls[name].ExecuteTemplate(&buf, "layout.html", data); err != nil {
				t.Fatalf("render page %q (%s): %v", name, lang, err)
			}
		}
		var buf bytes.Buffer
		if err := publicTmpl.Execute(&buf, map[string]any{"AssetVer": "test", "Lang": lang}); err != nil {
			t.Fatalf("render public (%s): %v", lang, err)
		}
	}
	for _, f := range fragments {
		if fragTmpls[f] == nil {
			t.Fatalf("fragment %q not built", f)
		}
	}

	// New fragments carry their own data shape — render both to guard them.
	frags := map[string]map[string]any{
		"settings": {"Lang": "id", "CanMutate": true, "Saved": true,
			"Routing": map[string]any{"Mode": "DEFAULT_OP", "Operator": "TRI", "Owned": []models.Operator{models.OpTri, models.OpTelkomsel}}},
		"fleet_detail": {"Lang": "en", "CanMutate": true,
			"Dev": deviceView{D: models.Device{Name: "HP-A"}, Sims: []models.Sim{{Operator: models.OpTri, Status: models.SimReady, DailyQuota: 300}}}},
	}
	for name, data := range frags {
		var buf bytes.Buffer
		if err := fragTmpls[name].ExecuteTemplate(&buf, name, data); err != nil {
			t.Fatalf("render fragment %q: %v", name, err)
		}
	}
}
