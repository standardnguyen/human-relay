package safety_test

import (
	"encoding/json"
	"testing"

	"github.com/standardnguyen/human-relay/tui/api"
	"github.com/standardnguyen/human-relay/tui/safety"
)

// The class below is the dashboard's rule, quoted: a string is flagged when it
// has a character outside
//
//	/[^\x20-\x7E\n\r\t\u2013\u2014\u2018\u2019\u201C\u201D\u2026]/
//
// i.e. printable ASCII, newline, carriage return, tab, en dash, em dash, the
// four curly quotes and the ellipsis.
func TestHasNonASCIIClass(t *testing.T) {
	ok := []string{"", "plain", "a b\tc\nd\re", "~", " ", "\u2013\u2014\u2018\u2019\u201c\u201d\u2026"}
	for _, s := range ok {
		if safety.HasNonASCII(s) {
			t.Errorf("%q should be clean", s)
		}
	}
	bad := []string{
		"\u0430", "\x00", "\x1b", "\x7f", "\x0b", "\x0c", "\x80", "\u00a0", "\u00e9",
		"\u200b", "\u202e", "\ufeff", "\u2012", "\u2015", "\u201a", "\u201b", "\u2025", "\U0001F600",
		"\xff",
	}
	for _, s := range bad {
		if !safety.HasNonASCII(s) {
			t.Errorf("%q should be flagged", s)
		}
	}
}

func mustReq(t *testing.T, raw string) api.Request {
	t.Helper()
	var r api.Request
	// Some fixtures put non-ASCII where a timestamp belongs; the struct decode
	// may fail on those, and only Raw matters to the checks under test.
	_ = json.Unmarshal([]byte(raw), &r)
	r.Raw = json.RawMessage(raw)
	return r
}

func TestRequestHasNonASCIIFieldCoverage(t *testing.T) {
	flagged := map[string]string{
		"command":          `{"command":"l\u0455"}`,
		"args":             `{"args":["ok","\u0430"]}`,
		"reason":           `{"reason":"p\u0430y"}`,
		"client":           `{"client":"\u0430"}`,
		"working_dir":      `{"working_dir":"/t\u043cp"}`,
		"display_command":  `{"display_command":"\u0430"}`,
		"deny_reason":      `{"deny_reason":"\u0430"}`,
		"withdraw_reason":  `{"withdraw_reason":"\u0430"}`,
		"http_url":         `{"http_url":"https://\u0430.example/"}`,
		"http_body":        `{"http_body":"\u0430"}`,
		"http_headers val": `{"http_headers":{"X":"\u0430"}}`,
		"http_headers key": `{"http_headers":{"\u0430":"x"}}`,
		"form_file":        `{"http_form_file":{"field":"f","filename":"a","fetch_cmd":["cat","\u0430"],"source":"s"}}`,
		"form_fields":      `{"http_form_fields":{"k":"\u0430"}}`,
		"script_name":      `{"script_name":"\u0430"}`,
		"script_args":      `{"script_args":["\u0430"]}`,
		"registry_op":      `{"registry_op":"\u0430"}`,
		"registry_args":    `{"registry_args":{"host":"\u0430"}}`,
		"stdin_sha256":     `{"stdin_sha256":"\u0430"}`,
		"unknown field":    `{"brand_new_field":"\u0430"}`,
		"nested unknown":   `{"brand_new":{"a":[{"b":"\u0430"}]}}`,
		"control char":     `{"command":"a\u001bb"}`,
		"method":           `{"http_method":"G\u0415T"}`,
	}
	for name, raw := range flagged {
		if !safety.RequestHasNonASCII(mustReq(t, raw)) {
			t.Errorf("%s: not flagged", name)
		}
	}
	// The structural fields the dashboard skips never flag, and neither does
	// command output.
	clean := []string{
		`{"id":"\u0430","type":"\u0430","status":"\u0430","created_at":"\u0430","decided_at":"\u0430"}`,
		`{"result":{"stdout":"\u0430 caf\u00e9","stderr":"\u0430","response_headers":{"\u0430":"\u0430"}}}`,
		`{"command":"ls","args":["-l","a \u2013 b"],"reason":"fine \u2014 really"}`,
		`{"command":"ls","args":null,"shell":false,"timeout":5,"stdin_len":3,"output_gated":true}`,
	}
	for _, raw := range clean {
		if safety.RequestHasNonASCII(mustReq(t, raw)) {
			t.Errorf("%s: flagged but should not be", raw)
		}
	}
}

func TestNonASCIIDepthLimit(t *testing.T) {
	// valueHasNonAscii returns false past depth 8; a hit at depth 8 still counts.
	deep := func(levels int, leaf string) string {
		s := `"` + leaf + `"`
		for i := 0; i < levels; i++ {
			s = `[` + s + `]`
		}
		return s
	}
	// Top-level field value is depth 0; each array adds one.
	if !safety.RequestHasNonASCII(mustReq(t, `{"x":`+deep(8, `\u0430`)+`}`)) {
		t.Error("depth 8 should be walked")
	}
	if safety.RequestHasNonASCII(mustReq(t, `{"x":`+deep(9, `\u0430`)+`}`)) {
		t.Error("depth 9 is past the walk limit")
	}
}

func TestNonASCIIFindingsNameFieldAndRunes(t *testing.T) {
	r := mustReq(t, `{"command":"l\u0455","args":["a","b\u0430\u0430"],"http_headers":{"X-\u0430":"v"},"result":{"stdout":"\u0430"}}`)
	fs := safety.NonASCIIFindings(r)
	got := map[string][]rune{}
	for _, f := range fs {
		got[f.Field] = f.Runes
	}
	if len(got["command"]) != 1 || got["command"][0] != 0x0455 {
		t.Fatalf("findings = %+v", fs)
	}
	if len(got["args"]) != 1 || got["args"][0] != 0x0430 {
		t.Fatalf("args finding should list each distinct rune once: %+v", fs)
	}
	if _, ok := got["http_headers"]; !ok {
		t.Fatalf("map keys are checked too: %+v", fs)
	}
	if _, ok := got["result"]; ok {
		t.Fatalf("result is not checked: %+v", fs)
	}
	// Stable order.
	if fs[0].Field > fs[len(fs)-1].Field {
		t.Fatalf("findings should be sorted by field: %+v", fs)
	}
}

func TestRequestWithoutRawFallsBackToStruct(t *testing.T) {
	r := api.Request{Command: "l\u0455"}
	if !safety.RequestHasNonASCII(r) {
		t.Fatal("a request built in code (no Raw) must still be checked")
	}
}

func TestNonASCIIRunes(t *testing.T) {
	got := safety.NonASCIIRunes("a\u0430b\u0430c\u0455 \u2014 ok")
	if len(got) != 2 || got[0] != 0x0430 || got[1] != 0x0455 {
		t.Fatalf("got %U", got)
	}
	if len(safety.NonASCIIRunes("plain")) != 0 {
		t.Fatal("plain text has none")
	}
}
