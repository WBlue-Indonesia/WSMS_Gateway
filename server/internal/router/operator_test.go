package router

import (
	"testing"

	"github.com/nizwar/wsms-gateway/server/internal/models"
)

func TestNormalizeMSISDN(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"0812-3456-7890", "+6281234567890", true},
		{"+62 812 3456 7890", "+6281234567890", true},
		{"6281234567890", "+6281234567890", true},
		{"81234567890", "+6281234567890", true},
		{"0812 3456 789", "+628123456789", true},   // spaces stripped
		{"+6289660517046", "+6289660517046", true}, // real number, +628 form
		{"1234", "", false},                        // not Indonesian mobile
		{"+18005551234", "", false},                // US number
		{"", "", false},
		// +62 must be followed by a mobile 8 — reject double-applied country codes.
		{"+626281299951524", "", false}, // reported bug: 62 country code double-applied
		{"+626289507718023", "", false}, // same shape, seen failing at the radio
		{"626281299951524", "", false},  // 62… but 62 6… → not +628
		{"06281299951524", "", false},   // 0 + 628… → +6262… after prefixing
	}
	for _, c := range cases {
		got, ok := NormalizeMSISDN(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("NormalizeMSISDN(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestDetectOperator(t *testing.T) {
	cases := []struct {
		in   string // canonical
		want models.Operator
	}{
		{"+6281234567890", models.OpTelkomsel}, // 0812
		{"+6285512345678", models.OpIndosat},   // 0855
		{"+6281712345678", models.OpXL},        // 0817
		{"+6283112345678", models.OpAxis},      // 0831
		{"+6289512345678", models.OpTri},       // 0895
		{"+6288112345678", models.OpSmartfren}, // 0881
		{"+6280012345678", models.OpUnknown},   // 0800 not mapped
	}
	for _, c := range cases {
		if got := DetectOperator(c.in, nil); got != c.want {
			t.Errorf("DetectOperator(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestWorkedExample mirrors docs/03: target 0812 (Telkomsel) with a fleet where
// HP A has {Telkomsel, XL} and HP B has {Indosat, Tri} → the Telkomsel SIM is on-net.
func TestWorkedExampleDetection(t *testing.T) {
	target, ok := NormalizeMSISDN("081298765432")
	if !ok {
		t.Fatal("normalize failed")
	}
	if op := DetectOperator(target, nil); op != models.OpTelkomsel {
		t.Fatalf("expected on-net operator TELKOMSEL, got %v", op)
	}
	// No-match fallback: a Smartfren target with only Telkomsel/XL/Indosat/Tri SIMs online
	// detects SMARTFREN, and Route() (DB-backed) would fall back to a random READY SIM.
	fbTarget, _ := NormalizeMSISDN("088812345678")
	if op := DetectOperator(fbTarget, nil); op != models.OpSmartfren {
		t.Fatalf("expected SMARTFREN, got %v", op)
	}
}
