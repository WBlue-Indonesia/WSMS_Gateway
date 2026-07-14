package admin

import (
	"bytes"
	"testing"
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
	for name, data := range pages {
		data["Page"] = name
		data["AssetVer"] = "test"
		data["User"] = map[string]any{"username": "admin", "role": "owner"}
		var buf bytes.Buffer
		if err := pageTmpls[name].ExecuteTemplate(&buf, "layout.html", data); err != nil {
			t.Fatalf("render page %q: %v", name, err)
		}
	}
	var buf bytes.Buffer
	if err := publicTmpl.Execute(&buf, map[string]any{"AssetVer": "test"}); err != nil {
		t.Fatalf("render public: %v", err)
	}
	for _, f := range fragments {
		if fragTmpls[f] == nil {
			t.Fatalf("fragment %q not built", f)
		}
	}
}
