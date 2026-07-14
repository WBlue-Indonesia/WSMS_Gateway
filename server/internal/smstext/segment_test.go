package smstext

import "testing"

func TestAnalyze(t *testing.T) {
	cases := []struct {
		body    string
		enc     string
		seg     int
	}{
		{"", GSM7, 1},
		{"Hello, your OTP is 123456", GSM7, 1},
		{repeat("a", 160), GSM7, 1},
		{repeat("a", 161), GSM7, 2},
		{repeat("a", 306), GSM7, 2},
		{repeat("a", 307), GSM7, 3},
		{"Kode OTP: 123456 berlaku 5 menit", GSM7, 1},
		{"emoji 🎉 forces unicode", UCS2, 1},
		{repeat("é", 70), GSM7, 1}, // é is in GSM basic set
		{repeat("あ", 70), UCS2, 1},
		{repeat("あ", 71), UCS2, 2},
	}
	for _, c := range cases {
		enc, seg := Analyze(c.body)
		if enc != c.enc || seg != c.seg {
			t.Errorf("Analyze(len=%d) = (%s,%d), want (%s,%d)", len([]rune(c.body)), enc, seg, c.enc, c.seg)
		}
	}
}

func repeat(s string, n int) string {
	out := make([]rune, 0, n)
	r := []rune(s)
	for i := 0; i < n; i++ {
		out = append(out, r[i%len(r)])
	}
	return string(out)
}
