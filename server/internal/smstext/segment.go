// Package smstext computes SMS encoding and segment count (docs/02 §0.5).
// The server computes this at submit time; clients are never trusted for cost.
package smstext

import "unicode/utf16"

// GSM 03.38 basic character set (each = 1 septet).
const gsm7Basic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ ÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
	"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

// GSM 03.38 extension set (each = 2 septets: ESC + char).
const gsm7Ext = "^{}\\[~]|€"

var (
	basicSet = func() map[rune]bool {
		m := make(map[rune]bool)
		for _, r := range gsm7Basic {
			m[r] = true
		}
		return m
	}()
	extSet = func() map[rune]bool {
		m := make(map[rune]bool)
		for _, r := range gsm7Ext {
			m[r] = true
		}
		return m
	}()
)

// Encoding names match models.Encoding values without importing models (avoids a cycle).
const (
	GSM7 = "GSM7"
	UCS2 = "UCS2"
)

// Analyze returns the encoding ("GSM7"/"UCS2") and the number of SMS segments.
func Analyze(body string) (encoding string, segments int) {
	septets := 0
	gsm := true
	for _, r := range body {
		if basicSet[r] {
			septets++
		} else if extSet[r] {
			septets += 2
		} else {
			gsm = false
			break
		}
	}
	if gsm {
		return GSM7, segCount(septets, 160, 153)
	}
	// UCS-2: count UTF-16 code units (surrogate pairs = 2).
	units := len(utf16.Encode([]rune(body)))
	return UCS2, segCount(units, 70, 67)
}

func segCount(n, single, multi int) int {
	if n == 0 {
		return 1
	}
	if n <= single {
		return 1
	}
	return (n + multi - 1) / multi
}
