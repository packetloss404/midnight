package acme

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CAAClassification reports how a parsed CAA record set treats issuance for a
// configured CA host (e.g., "letsencrypt.org"). It is the offline classifier
// used by the CAA preflight gate.
type CAAClassification string

const (
	// CAAClassificationPermits means the CAA record set explicitly authorizes the configured CA.
	CAAClassificationPermits CAAClassification = "permits"
	// CAAClassificationForbids means the CAA record set forbids any CA, the
	// configured CA, or every authority via "0 issue \";\"".
	CAAClassificationForbids CAAClassification = "forbids"
	// CAAClassificationAmbiguous means the record set is unrecognized or
	// contains conflicting tags; callers must escalate before issuance.
	CAAClassificationAmbiguous CAAClassification = "ambiguous"
)

// ParseCAARecords parses CAA record textual representations. Each input line
// is one record in the standard zone-file form `<flags> <tag> "<value>"`.
// Whitespace, blank lines, and `;` comments are tolerated.
func ParseCAARecords(input string) ([]CAARecord, error) {
	lines := strings.Split(input, "\n")
	out := make([]CAARecord, 0, len(lines))
	for index, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip trailing zone-file comments after a `;`, but only when the
		// `;` sits outside the quoted value: RFC 8657 parameters (e.g.
		// `accounturi=`) are themselves `;`-separated inside the quotes and
		// must survive parsing.
		if idx := indexOutsideQuotes(line, ';'); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
			if line == "" {
				continue
			}
		}
		record, err := parseCAALine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", index+1, err)
		}
		out = append(out, record)
	}
	return out, nil
}

func parseCAALine(line string) (CAARecord, error) {
	// Expected form: `<flags> <tag> "<value>"` with optional spaces.
	first := strings.IndexAny(line, " \t")
	if first < 0 {
		return CAARecord{}, errors.New("CAA record requires flags, tag, and value")
	}
	flagsStr := line[:first]
	rest := strings.TrimSpace(line[first:])
	second := strings.IndexAny(rest, " \t")
	if second < 0 {
		return CAARecord{}, errors.New("CAA record requires tag and value")
	}
	tag := strings.TrimSpace(rest[:second])
	value := strings.TrimSpace(rest[second:])

	flags, err := strconv.Atoi(flagsStr)
	if err != nil {
		return CAARecord{}, fmt.Errorf("CAA flags must be an integer: %w", err)
	}
	if flags < 0 || flags > 255 {
		return CAARecord{}, errors.New("CAA flags must fit in one byte")
	}
	if tag == "" {
		return CAARecord{}, errors.New("CAA tag is required")
	}
	tag = strings.ToLower(tag)
	if value == "" {
		return CAARecord{}, errors.New("CAA value is required")
	}
	value = unquoteCAAValue(value)
	return CAARecord{
		Tag:      tag,
		Value:    value,
		Critical: flags&0x80 != 0,
	}, nil
}

// indexOutsideQuotes returns the index of the first occurrence of target that
// is not enclosed in double quotes, or -1 if there is none.
func indexOutsideQuotes(s string, target byte) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case target:
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

func unquoteCAAValue(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return value[1 : len(value)-1]
	}
	return value
}

// classifyCAAConfig carries optional inputs for ClassifyCAARecords. It is
// populated by ClassifyCAAOption values so that callers who only care about
// host matching can keep using the two-argument form.
type classifyCAAConfig struct {
	accountURI        string
	validationMethods map[string]struct{}
}

// ClassifyCAAOption customizes ClassifyCAARecords without breaking the existing
// two-argument callers.
type ClassifyCAAOption func(*classifyCAAConfig)

// WithACMEAccountURI binds classification to a specific ACME account URI so that
// RFC 8657 `accounturi=` parameters on `issue`/`issuewild` records are honored:
// a record that pins an account only authorizes when the URI matches.
func WithACMEAccountURI(accountURI string) ClassifyCAAOption {
	return func(cfg *classifyCAAConfig) {
		cfg.accountURI = strings.TrimSpace(accountURI)
	}
}

// WithACMEValidationMethod declares the validation method the ACME client used
// (e.g. "http-01", "dns-01"). When set, RFC 8657 `validationmethods=`
// parameters are honored: a record that pins methods only authorizes when the
// declared method is among them. When unset, `validationmethods=` is parsed but
// not enforced, preserving the offline, caller-supplied contract.
func WithACMEValidationMethod(method string) ClassifyCAAOption {
	return func(cfg *classifyCAAConfig) {
		method = strings.ToLower(strings.TrimSpace(method))
		if method == "" {
			return
		}
		if cfg.validationMethods == nil {
			cfg.validationMethods = make(map[string]struct{})
		}
		cfg.validationMethods[method] = struct{}{}
	}
}

// parseCAAIssueValue splits an `issue`/`issuewild` value into its CA host and
// RFC 8657 parameters. The value form is `<ca-host>[; k=v [; k=v ...]]`. Keys
// are lower-cased; the host is trimmed but left otherwise as written. An empty
// host (e.g. the bare `;` "issue nothing" form) is returned as "".
func parseCAAIssueValue(value string) (string, map[string]string) {
	parts := strings.Split(value, ";")
	caHost := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return caHost, nil
	}
	params := make(map[string]string)
	for _, raw := range parts[1:] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		key, val, ok := strings.Cut(raw, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if !ok {
			params[key] = ""
			continue
		}
		params[key] = strings.TrimSpace(val)
	}
	return caHost, params
}

// caaParamsAuthorize reports whether an issue/issuewild record for the matched
// CA host is permitted for this ACME client given its RFC 8657 parameters.
func caaParamsAuthorize(params map[string]string, cfg classifyCAAConfig) bool {
	if params == nil {
		return true
	}
	if accountURI, ok := params["accounturi"]; ok {
		if strings.TrimSpace(accountURI) != cfg.accountURI {
			return false
		}
	}
	if methods, ok := params["validationmethods"]; ok && len(cfg.validationMethods) > 0 {
		matched := false
		for _, method := range strings.Split(methods, ",") {
			method = strings.ToLower(strings.TrimSpace(method))
			if method == "" {
				continue
			}
			if _, present := cfg.validationMethods[method]; present {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// ClassifyCAARecords inspects parsed CAA records and reports whether the
// configured CA host is permitted, forbidden, or the result is ambiguous.
// `ca` is matched case-insensitively against `issue`/`issuewild` values.
//
// RFC 8657 parameters (`accounturi=`, `validationmethods=`) on a matching
// record are honored when the caller supplies the corresponding option
// (WithACMEAccountURI / WithACMEValidationMethod): such a record only authorizes
// when its bindings are satisfied. Without those options, parameters are parsed
// but not enforced, so a record with no parameters behaves exactly as before.
// This classifier remains fully offline and never performs DNS resolution.
func ClassifyCAARecords(records []CAARecord, ca string, opts ...ClassifyCAAOption) CAAClassification {
	var cfg classifyCAAConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	ca = strings.ToLower(strings.TrimSpace(ca))
	if ca == "" {
		return CAAClassificationAmbiguous
	}
	if len(records) == 0 {
		// No CAA records means any CA is permitted by RFC 6844.
		return CAAClassificationPermits
	}
	hasIssue := false
	permits := false
	forbids := false
	hasUnknownCritical := false
	for _, record := range records {
		switch record.Tag {
		case "issue", "issuewild":
			hasIssue = true
			// Parse from the raw value: the CA host is compared
			// case-insensitively, but parameter values such as `accounturi=`
			// are case-sensitive (URIs) and must not be folded.
			value := strings.TrimSpace(record.Value)
			if value == ";" || value == "" {
				forbids = true
				continue
			}
			caHost, params := parseCAAIssueValue(value)
			if caHost == "" {
				// A value that resolves to no CA host (e.g. `; accounturi=...`)
				// forbids issuance, mirroring the bare `;` form.
				forbids = true
				continue
			}
			if strings.ToLower(caHost) == ca && caaParamsAuthorize(params, cfg) {
				permits = true
			}
		case "iodef":
			// iodef does not affect issuance permission.
		default:
			if record.Critical {
				hasUnknownCritical = true
			}
		}
	}
	if hasUnknownCritical {
		return CAAClassificationAmbiguous
	}
	if !hasIssue {
		// No issue/issuewild tags but other tags present means any CA may issue.
		return CAAClassificationPermits
	}
	if permits {
		return CAAClassificationPermits
	}
	if forbids {
		return CAAClassificationForbids
	}
	// Issue tags exist but reference a different CA -> forbidden for `ca`.
	return CAAClassificationForbids
}

// CAAPreflight computes a CAAPreflightResult from parsed records and a
// classification. The result is suitable for storage in DomainDiagnostics.
func CAAPreflight(domain, ca, raw string, opts ...ClassifyCAAOption) (CAAPreflightResult, CAAClassification, error) {
	records, err := ParseCAARecords(raw)
	if err != nil {
		return CAAPreflightResult{}, CAAClassificationAmbiguous, err
	}
	classification := ClassifyCAARecords(records, ca, opts...)
	result := CAAPreflightResult{
		Domain:  normalizeDomain(domain),
		Records: append([]CAARecord(nil), records...),
	}
	switch classification {
	case CAAClassificationPermits:
		result.Status = DomainCAAAllowed
	case CAAClassificationForbids:
		result.Status = DomainCAABlocked
	default:
		result.Status = DomainCAAUnknown
	}
	sort.SliceStable(result.Records, func(i, j int) bool {
		if result.Records[i].Tag != result.Records[j].Tag {
			return result.Records[i].Tag < result.Records[j].Tag
		}
		return result.Records[i].Value < result.Records[j].Value
	})
	return result, classification, nil
}
