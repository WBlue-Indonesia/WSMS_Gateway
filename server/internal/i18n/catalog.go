package i18n

// catalog[lang][key] = translated string. English is the fallback source: every key
// SHOULD exist under "en"; "id" overrides for Indonesian. Entries are registered from
// init() in the per-area catalog_*.go files so translations can be split across files.
var catalog = map[string]map[string]string{
	"en": {},
	"id": {},
}

// register merges entries into the catalog for a language (called from init()).
func register(lang string, entries map[string]string) {
	m := catalog[lang]
	if m == nil {
		m = map[string]string{}
		catalog[lang] = m
	}
	for k, v := range entries {
		m[k] = v
	}
}
