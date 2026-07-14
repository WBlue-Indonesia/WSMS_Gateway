// Package router implements operator-aware SIM selection (docs/03-routing-engine.md).
package router

import (
	"regexp"
	"strings"

	"github.com/nizwar/wsms-gateway/server/internal/models"
)

// DefaultPrefixes is the seed prefix->operator table (08xx form). Persisted to
// operator_prefixes and editable at runtime; this constant is the fallback/seed.
//
// Caveat (docs/03 §2): Indonesia has no full mobile MNP, so prefix->operator is a
// strong heuristic, not a guarantee — this is exactly why routing has a fallback.
// INDOSAT and TRI stay distinct despite the 2022 IOH merger (separate networks).
var DefaultPrefixes = map[string]models.Operator{
	// Telkomsel
	"0811": models.OpTelkomsel, "0812": models.OpTelkomsel, "0813": models.OpTelkomsel,
	"0821": models.OpTelkomsel, "0822": models.OpTelkomsel, "0823": models.OpTelkomsel,
	"0851": models.OpTelkomsel, "0852": models.OpTelkomsel, "0853": models.OpTelkomsel,
	// Indosat (IM3)
	"0814": models.OpIndosat, "0815": models.OpIndosat, "0816": models.OpIndosat,
	"0855": models.OpIndosat, "0856": models.OpIndosat, "0857": models.OpIndosat, "0858": models.OpIndosat,
	// XL Axiata
	"0817": models.OpXL, "0818": models.OpXL, "0819": models.OpXL,
	"0859": models.OpXL, "0877": models.OpXL, "0878": models.OpXL,
	// Axis
	"0831": models.OpAxis, "0832": models.OpAxis, "0833": models.OpAxis, "0838": models.OpAxis,
	// Tri (3)
	"0895": models.OpTri, "0896": models.OpTri, "0897": models.OpTri, "0898": models.OpTri, "0899": models.OpTri,
	// Smartfren
	"0881": models.OpSmartfren, "0882": models.OpSmartfren, "0883": models.OpSmartfren,
	"0884": models.OpSmartfren, "0885": models.OpSmartfren, "0886": models.OpSmartfren,
	"0887": models.OpSmartfren, "0888": models.OpSmartfren, "0889": models.OpSmartfren,
}

var (
	stripRe = regexp.MustCompile(`[\s\-().]+`)
	e164Re  = regexp.MustCompile(`^\+62\d{8,13}$`)
)

// NormalizeMSISDN converts any Indonesian mobile input to canonical E.164 (+62...).
// Returns ("", false) if the number cannot be normalized to a valid ID mobile number.
func NormalizeMSISDN(raw string) (string, bool) {
	s := stripRe.ReplaceAllString(strings.TrimSpace(raw), "")
	switch {
	case strings.HasPrefix(s, "+62"):
		// keep
	case strings.HasPrefix(s, "62"):
		s = "+" + s
	case strings.HasPrefix(s, "0"):
		s = "+62" + s[1:]
	case strings.HasPrefix(s, "8"):
		s = "+62" + s
	default:
		return "", false
	}
	if !e164Re.MatchString(s) {
		return "", false
	}
	return s, true
}

// localForm derives the 0xxx national form from canonical +62 form.
func localForm(canonical string) string {
	if strings.HasPrefix(canonical, "+62") {
		return "0" + canonical[3:]
	}
	return canonical
}

// CarrierToOperator maps an Android SubscriptionInfo carrierName to our operator enum.
// carrierName is unreliable (docs/03 §6) — a per-SIM manual override (Sim.OperatorLocked)
// takes precedence over whatever this returns.
func CarrierToOperator(name string) models.Operator {
	n := strings.ToUpper(strings.TrimSpace(name))
	switch {
	case strings.Contains(n, "TELKOMSEL") || strings.Contains(n, "TSEL") || strings.Contains(n, "SIMPATI"):
		return models.OpTelkomsel
	case strings.Contains(n, "INDOSAT") || strings.Contains(n, "IM3") || strings.Contains(n, "OOREDOO"):
		return models.OpIndosat
	case strings.Contains(n, "XL"):
		return models.OpXL
	case strings.Contains(n, "AXIS"):
		return models.OpAxis
	case n == "3" || strings.Contains(n, "TRI") || strings.Contains(n, "HUTCHISON") || strings.Contains(n, "H3I"):
		return models.OpTri
	case strings.Contains(n, "SMARTFREN") || strings.Contains(n, "SMART"):
		return models.OpSmartfren
	}
	return models.OpUnknown
}

// DetectOperator returns the operator for a canonical +62 MSISDN using the given
// prefix table (pass nil to use DefaultPrefixes). Returns OpUnknown on no match.
func DetectOperator(canonical string, table map[string]models.Operator) models.Operator {
	if table == nil {
		table = DefaultPrefixes
	}
	local := localForm(canonical)
	if len(local) < 4 {
		return models.OpUnknown
	}
	if op, ok := table[local[:4]]; ok {
		return op
	}
	return models.OpUnknown
}
