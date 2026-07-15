// Package i18n is a tiny message catalog for the admin console. Two UI languages:
// Indonesian (id, the default) and English (en). Lookups fall back id→en→key, so a
// missing translation degrades to English and never to a blank or a template error.
package i18n

import (
	"net/http"
	"strings"
)

// CookieName holds the operator's chosen UI language; Path=/ so it covers both the
// /admin console and the public "/" page.
const CookieName = "wsms_lang"

// Default is the UI language when nothing else is signalled.
const Default = "id"

// Lang is an offered UI language (code + short display label for the switcher).
type Lang struct{ Code, Label string }

// Supported lists the offered languages in display order.
var Supported = []Lang{
	{"id", "ID"},
	{"en", "EN"},
}

// IsSupported reports whether code is an offered language.
func IsSupported(code string) bool {
	for _, l := range Supported {
		if l.Code == code {
			return true
		}
	}
	return false
}

// T returns the message for key in lang, falling back to English, then to the key
// itself so an un-keyed string is visible rather than blank.
func T(lang, key string) string {
	if m, ok := catalog[lang]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	if s, ok := catalog["en"][key]; ok {
		return s
	}
	return key
}

// Resolve picks the UI language from (in priority) the lang cookie, then the
// Accept-Language header, defaulting to Default.
func Resolve(r *http.Request) string {
	if c, err := r.Cookie(CookieName); err == nil && IsSupported(c.Value) {
		return c.Value
	}
	al := strings.ToLower(r.Header.Get("Accept-Language"))
	switch {
	case strings.HasPrefix(al, "en"):
		return "en"
	case strings.HasPrefix(al, "id"):
		return "id"
	}
	return Default
}
