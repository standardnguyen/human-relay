package safety

import (
	"encoding/json"
	"sort"

	"github.com/standardnguyen/human-relay/tui/api"
)

// A request is flagged when any of its fields, keys and values included, has a
// character outside this class (the same rule the dashboard applies):
//
//	/[^\x20-\x7E\n\r\t\u2013\u2014\u2018\u2019\u201C\u201D\u2026]/
//
// Printable ASCII, newline, carriage return, tab, en and em dash, curly
// quotes and the ellipsis are plain text; everything else, control characters
// and invalid UTF-8 included, is flagged.

// HasNonASCII reports whether s has a character outside the plain-text class.
func HasNonASCII(s string) bool {
	for _, r := range s {
		if !allowedRune(r) {
			return true
		}
	}
	return false
}

// skipFields are structural fields the relay sets itself (identifiers, enum
// values, timestamps) plus command output, which is not part of the decision.
// Every other field is walked, including ones added to the API later.
var skipFields = map[string]bool{
	"id": true, "type": true, "status": true, "created_at": true, "decided_at": true, "result": true,
}

const maxWalkDepth = 8

// Finding names a field and the distinct flagged characters found in it.
type Finding struct {
	Field string
	Runes []rune
}

func requestMap(r api.Request) map[string]any {
	raw := []byte(r.Raw)
	if len(raw) == 0 {
		raw, _ = json.Marshal(r)
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func collect(v any, depth int, seen map[rune]bool, order *[]rune) {
	if depth > maxWalkDepth {
		return
	}
	add := func(s string) {
		for _, r := range s {
			if !allowedRune(r) && !seen[r] {
				seen[r] = true
				*order = append(*order, r)
			}
		}
	}
	switch t := v.(type) {
	case string:
		add(t)
	case []any:
		for _, e := range t {
			collect(e, depth+1, seen, order)
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			add(k)
			collect(t[k], depth+1, seen, order)
		}
	}
}

// NonASCIIFindings lists, per field, the distinct characters that made the
// request non-ASCII. Fields come back sorted by name.
func NonASCIIFindings(r api.Request) []Finding {
	m := requestMap(r)
	fields := make([]string, 0, len(m))
	for k := range m {
		if !skipFields[k] {
			fields = append(fields, k)
		}
	}
	sort.Strings(fields)
	var out []Finding
	for _, k := range fields {
		seen := map[rune]bool{}
		var order []rune
		collect(m[k], 0, seen, &order)
		if len(order) > 0 {
			out = append(out, Finding{Field: k, Runes: order})
		}
	}
	return out
}

// RequestHasNonASCII reports whether NonASCIIFindings would find anything.
func RequestHasNonASCII(r api.Request) bool { return len(NonASCIIFindings(r)) > 0 }

// NonASCIIRunes returns the distinct characters of s that are outside the
// plain-text class, in order of first appearance.
func NonASCIIRunes(s string) []rune {
	seen := map[rune]bool{}
	var out []rune
	for _, r := range s {
		if !allowedRune(r) && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}
