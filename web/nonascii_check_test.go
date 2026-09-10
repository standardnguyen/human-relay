package web

// Tests for the approval dashboard's homoglyph/bidi warning check
// (requestHasNonAscii in templates/index.html).
//
// Security finding #24: the check used to name r.command / r.reason /
// r.working_dir / r.args by hand, so every field added to store.Request
// afterwards silently escaped it -- http_url, http_body, display_command,
// script_args, and then registry_op / registry_args. Those fields are rendered
// in the approval card, so a right-to-left override or a Cyrillic homoglyph in
// a URL could disguise what the reviewer was approving. The fix walks the
// request object generically; these tests pin that behavior.
//
// The check is JavaScript, so the tests extract it straight out of the
// embedded template and execute it under node. That is the only way to
// actually exercise it -- a Go reimplementation would test the copy, not the
// code that ships. If node is not installed the tests skip loudly; they were
// developed and run against node v26.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/store"
)

// Boundaries of the region of index.html holding the non-ASCII check and its
// helpers. Extracting a slice rather than the whole <script> keeps the code
// runnable outside a browser (the rest of the script touches document/fetch).
const (
	nonAsciiJSStart = "function hasNonAscii(s) {"
	nonAsciiJSEnd   = "function formatSize(bytes) {"
)

// Characters that must trip the check. Written as escapes on purpose: the RLO
// is invisible and would scramble how this file renders if pasted literally.
const (
	cyrillicA = "а" // Cyrillic small A -- homoglyph for ASCII 'a'
	rlo       = "‮" // RIGHT-TO-LEFT OVERRIDE
)

// extractNonAsciiJS pulls the check + its helpers out of the embedded template.
func extractNonAsciiJS(t *testing.T) string {
	t.Helper()
	raw, err := templateFS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read embedded templates/index.html: %v", err)
	}
	src := string(raw)
	i := strings.Index(src, nonAsciiJSStart)
	j := strings.Index(src, nonAsciiJSEnd)
	if i < 0 || j < 0 || j <= i {
		t.Fatalf("could not locate the non-ASCII check in templates/index.html "+
			"(start marker found=%v, end marker found=%v) -- if the code moved, "+
			"update nonAsciiJSStart/nonAsciiJSEnd", i >= 0, j >= 0)
	}
	return src[i:j]
}

// jsRun evaluates requestHasNonAscii over each case under node and returns the
// per-case verdicts plus the skip-list the JS actually declares.
func jsRun(t *testing.T, requests []map[string]any) (results []bool, skip []string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH -- cannot execute the dashboard JS; install node to run this test")
	}

	dir := t.TempDir()
	script := extractNonAsciiJS(t) + `
const fs = require('fs');
const cases = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
process.stdout.write(JSON.stringify({
  skip: Array.from(NONASCII_SKIP_FIELDS),
  results: cases.map(requestHasNonAscii),
}));
`
	scriptPath := filepath.Join(dir, "check.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write js: %v", err)
	}
	casesJSON, err := json.Marshal(requests)
	if err != nil {
		t.Fatalf("marshal cases: %v", err)
	}
	casesPath := filepath.Join(dir, "cases.json")
	if err := os.WriteFile(casesPath, casesJSON, 0o600); err != nil {
		t.Fatalf("write cases: %v", err)
	}

	out, err := exec.Command(node, scriptPath, casesPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node failed: %v\n%s", err, out)
	}
	var parsed struct {
		Skip    []string `json:"skip"`
		Results []bool   `json:"results"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse node output %q: %v", out, err)
	}
	if len(parsed.Results) != len(requests) {
		t.Fatalf("node returned %d results for %d cases", len(parsed.Results), len(requests))
	}
	return parsed.Results, parsed.Skip
}

// TestDashboardNonAsciiCheck covers the fields finding #24 named, the fields
// the old hand-written check already handled (regression pins), and the
// negatives that would make the warning useless through alert fatigue.
func TestDashboardNonAsciiCheck(t *testing.T) {
	cases := []struct {
		name string
		req  map[string]any
		want bool
	}{
		// --- finding #24: fields the hand-written check missed ---
		{"http_url homoglyph", map[string]any{
			"type": "http", "http_method": "POST",
			"http_url": "https://" + cyrillicA + "pi.trello.com/1/cards",
		}, true},
		{"http_body bidi override", map[string]any{
			"type": "http", "http_body": `{"text":"harmless` + rlo + `"}`,
		}, true},
		{"display_command homoglyph", map[string]any{
			"display_command": "rm -rf /v" + cyrillicA + "r/lib",
		}, true},
		{"script_args element", map[string]any{
			"type": "script", "script_name": "next-task",
			"script_args": []string{"--list", "in" + cyrillicA + "x"},
		}, true},
		{"script_name", map[string]any{
			"type": "script", "script_name": "deploy" + cyrillicA,
		}, true},
		{"registry_op", map[string]any{
			"type": "registry_op", "registry_op": "delete_cont" + cyrillicA + "iner",
		}, true},
		{"registry_args value", map[string]any{
			"type": "registry_op", "registry_op": "register_container",
			"registry_args": map[string]string{"name": "rel" + cyrillicA + "y"},
		}, true},
		{"registry_args key", map[string]any{
			"type": "registry_op", "registry_op": "register_container",
			"registry_args": map[string]string{"n" + cyrillicA + "me": "relay"},
		}, true},
		{"http_headers value", map[string]any{
			"type": "http", "http_url": "https://example.com",
			"http_headers": map[string]string{"X-Note": "ok" + rlo},
		}, true},
		{"http_form_fields value", map[string]any{
			"type": "http", "http_form_fields": map[string]string{"k": cyrillicA},
		}, true},
		{"http_form_file filename", map[string]any{
			"type": "http",
			"http_form_file": map[string]any{
				"field": "file", "filename": "repor" + rlo + "t.xlsx",
				"fetch_cmd": []string{"cat", "/tmp/x"}, "source": "CTID 115:/tmp/x",
			},
		}, true},
		{"http_form_file fetch_cmd element", map[string]any{
			"type": "http",
			"http_form_file": map[string]any{
				"field": "file", "filename": "r.xlsx",
				"fetch_cmd": []string{"cat", "/tmp/" + cyrillicA}, "source": "x",
			},
		}, true},
		{"http_method", map[string]any{
			"type": "http", "http_method": "P" + cyrillicA + "ST",
		}, true},
		{"withdraw_reason", map[string]any{
			"command": "echo", "withdraw_reason": "sup" + cyrillicA + "rseded",
		}, true},
		{"deny_reason", map[string]any{
			"command": "echo", "deny_reason": "n" + cyrillicA + "pe",
		}, true},

		// --- regression pins: what the old check already caught ---
		{"command", map[string]any{"command": "ec" + cyrillicA}, true},
		{"reason", map[string]any{"command": "echo", "reason": "beca" + cyrillicA + "se"}, true},
		{"working_dir", map[string]any{"command": "echo", "working_dir": "/r" + cyrillicA + "ot"}, true},
		{"args element", map[string]any{
			"command": "echo", "args": []string{"fine", "b" + rlo + "ad"},
		}, true},

		// --- negatives: structural metadata must not raise the warning ---
		{"plain ascii request", map[string]any{
			"id": "abc123", "type": "command", "command": "echo",
			"args": []string{"hello", "world"}, "reason": "test",
			"working_dir": "/root", "shell": false, "timeout": 30,
			"status": "pending", "created_at": "2026-09-10T12:00:00Z",
			"stdin_len": 0, "output_gated": false,
		}, false},
		{"non-ascii only in id/status/type/timestamps", map[string]any{
			"id": cyrillicA, "type": cyrillicA, "status": cyrillicA,
			"created_at": "2026-09-10T12:00:00" + cyrillicA,
			"decided_at": "2026-09-10T12:01:00" + cyrillicA,
			"command":    "echo", "reason": "clean",
		}, false},
		{"result output is not part of the approval decision", map[string]any{
			"command": "echo", "reason": "clean", "status": "complete",
			"result": map[string]any{
				"exit_code": 0, "stdout": "café ☃ а", "stderr": cyrillicA,
				"response_headers": map[string]string{"X-" + cyrillicA: cyrillicA},
			},
		}, false},
		{"typographic punctuation allowlist still holds", map[string]any{
			"command": "echo",
			"reason":  "it’s fine — really… “quoted” – yes",
			"http_body": "{\"t\":\"it’s fine — really…\"}",
		}, false},
		{"empty request", map[string]any{}, false},
	}

	reqs := make([]map[string]any, len(cases))
	for i, c := range cases {
		reqs[i] = c.req
	}
	results, _ := jsRun(t, reqs)

	for i, c := range cases {
		if results[i] != c.want {
			t.Errorf("requestHasNonAscii(%s) = %v, want %v", c.name, results[i], c.want)
		}
	}
}

// structuralSkipAllowed is the set of store.Request json fields this test
// agrees are structural metadata rather than reviewable content, and so may
// legitimately appear in the JS skip-list. It exists so that widening the JS
// skip-list to cover a *content* field -- which would reopen finding #24 by
// the back door -- fails here instead of shipping quietly.
var structuralSkipAllowed = map[string]string{
	"id":         "server-generated hex identifier",
	"type":       "server-set enum",
	"status":     "server-set enum",
	"created_at": "server-generated timestamp",
	"decided_at": "server-generated timestamp",
	"result":     "command output, not part of the approval decision",
}

// TestDashboardNonAsciiCoversRequestFields derives one probe per string-bearing
// field of store.Request by reflection and asserts the dashboard check trips on
// every one that isn't deliberately skipped. Deriving the cases from the Go
// struct (which is what web/handler.go serializes to the dashboard) means a
// field added later is tested automatically -- the exact failure mode that
// produced finding #24 twice.
func TestDashboardNonAsciiCoversRequestFields(t *testing.T) {
	type probe struct {
		field string
		req   map[string]any
	}
	var probes []probe

	rt := reflect.TypeOf(store.Request{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := jsonFieldName(f)
		if name == "" {
			continue
		}
		v, ok := probeValue(f.Type)
		if !ok {
			continue // not string-bearing in JSON (bool, int, ...)
		}
		probes = append(probes, probe{field: name, req: map[string]any{name: v}})
	}
	if len(probes) < 15 {
		t.Fatalf("reflection produced only %d probes; store.Request should have far more "+
			"string-bearing fields -- probeValue is probably wrong", len(probes))
	}

	reqs := make([]map[string]any, len(probes))
	for i, p := range probes {
		reqs[i] = p.req
	}
	results, skip := jsRun(t, reqs)

	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[s] = true
		if _, ok := structuralSkipAllowed[s]; !ok {
			t.Errorf("JS NONASCII_SKIP_FIELDS contains %q, which this test does not "+
				"consider structural metadata -- excluding a content field from the "+
				"homoglyph check reopens finding #24", s)
		}
	}

	for i, p := range probes {
		want := !skipSet[p.field]
		if results[i] != want {
			t.Errorf("field %q: requestHasNonAscii = %v, want %v (skipped=%v)",
				p.field, results[i], want, skipSet[p.field])
		}
	}
}

// jsonFieldName returns the JSON key a struct field serializes under, or ""
// if it is not serialized.
func jsonFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		if !f.IsExported() {
			return ""
		}
		return f.Name
	}
	return name
}

// probeValue builds a JSON value of the given Go type carrying a homoglyph,
// or reports false if the type cannot carry a string in JSON.
func probeValue(t reflect.Type) (any, bool) {
	marker := "probe" + cyrillicA
	switch t.Kind() {
	case reflect.String:
		return marker, true
	case reflect.Ptr:
		return probeValue(t.Elem())
	case reflect.Slice:
		// []byte lands in this Kind too, but its elem is Uint8, so only real
		// string slices (args, script_args, fetch_cmd) match here.
		if t.Elem().Kind() == reflect.String {
			return []any{marker}, true
		}
		return nil, false
	case reflect.Map:
		if t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.String {
			return map[string]any{"probe": marker}, true
		}
		return nil, false
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			return marker, true // marshals as an RFC3339 string
		}
		nested := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := jsonFieldName(f)
			if name == "" {
				continue
			}
			if v, ok := probeValue(f.Type); ok {
				nested[name] = v
			}
		}
		if len(nested) == 0 {
			return nil, false
		}
		return nested, true
	}
	return nil, false
}
