package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findByID returns the list_requests entry for id, failing the test if the
// request is missing from the listing entirely.
func findByID(t *testing.T, list []RequestResult, id string) RequestResult {
	t.Helper()
	for _, r := range list {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("request %s not present in list_requests output (%d entries)", id, len(list))
	return RequestResult{}
}

// TestListRequestsRedactsGatedOutput pins finding #7: list_requests marshaled
// the raw store records, so a single call handed the agent the real
// stdout/stderr of every output-gated request — a one-call bypass of the whole
// output-gating feature that get_result correctly honors.
//
// The list deliberately holds one gated and one ungated request: a redaction
// that fired on everything would pass a gated-only assertion, so the ungated
// entry is the control that proves the filter discriminates.
func TestListRequestsRedactsGatedOutput(t *testing.T) {
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "echo", "args": []string{"topsecret"}, "gate_output": true},
		{"command": "echo", "args": []string{"publicvalue"}},
	}
	data, _ := json.Marshal(rules)
	os.WriteFile(wlPath, data, 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"topsecret"},
			"reason":  "gated request for list_requests redaction test",
		},
	})
	gatedID := extractRequestID(t, resp)

	resp = c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"publicvalue"},
			"reason":  "ungated control for list_requests redaction test",
		},
	})
	ungatedID := extractRequestID(t, resp)

	if got := pollUntilDone(t, c, 4, gatedID); got.Status != "complete" {
		t.Fatalf("gated request status = %s, want complete", got.Status)
	}
	if got := pollUntilDone(t, c, 5, ungatedID); got.Status != "complete" {
		t.Fatalf("ungated request status = %s, want complete", got.Status)
	}

	listResp := c.Call(t, 6, "tools/call", map[string]interface{}{
		"name":      "list_requests",
		"arguments": map[string]interface{}{},
	})
	list := extractList(t, listResp)

	gated := findByID(t, list, gatedID)
	if !gated.OutputGated {
		t.Fatalf("expected output_gated true on the gated entry, got false")
	}
	if gated.Result == nil {
		t.Fatal("gated entry has no result")
	}
	if strings.Contains(gated.Result.Stdout, "topsecret") {
		t.Errorf("finding #7: list_requests leaked gated stdout: %q", gated.Result.Stdout)
	}
	if !strings.Contains(gated.Result.Stdout, "output gated by operator") {
		t.Errorf("expected gate placeholder in list_requests stdout, got %q", gated.Result.Stdout)
	}

	// Control: an ungated request in the same listing still shows real output.
	ungated := findByID(t, list, ungatedID)
	if ungated.Result == nil || ungated.Result.Stdout != "publicvalue\n" {
		t.Errorf("ungated entry stdout = %q, want %q — redaction must not apply to ungated requests",
			ungated.Result.Stdout, "publicvalue\n")
	}

	// After the operator releases, list_requests shows the real content again.
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/release", s.WebURL(), gatedID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("release returned status %d", code)
	}

	listResp = c.Call(t, 7, "tools/call", map[string]interface{}{
		"name":      "list_requests",
		"arguments": map[string]interface{}{},
	})
	released := findByID(t, extractList(t, listResp), gatedID)
	if released.OutputGated {
		t.Error("expected output_gated false after release")
	}
	if released.Result == nil || released.Result.Stdout != "topsecret\n" {
		t.Errorf("released stdout = %q, want %q", released.Result.Stdout, "topsecret\n")
	}
}

// TestListRequestsRedactsGatedStderr covers the stderr half of the same gate:
// a gated failure must not hand its stderr back through list_requests either.
func TestListRequestsRedactsGatedStderr(t *testing.T) {
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "sh", "args": []string{"-c", "echo secretdiagnostic >&2; exit 3"}, "gate_output": true},
	}
	data, _ := json.Marshal(rules)
	os.WriteFile(wlPath, data, 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "sh",
			"args":    []string{"-c", "echo secretdiagnostic >&2; exit 3"},
			"reason":  "gated stderr for list_requests redaction test",
		},
	})
	id := extractRequestID(t, resp)

	if got := pollUntilDone(t, c, 3, id); got.Status != "error" {
		t.Fatalf("status = %s, want error (exit 3)", got.Status)
	}

	listResp := c.Call(t, 4, "tools/call", map[string]interface{}{
		"name":      "list_requests",
		"arguments": map[string]interface{}{},
	})
	entry := findByID(t, extractList(t, listResp), id)
	if entry.Result == nil {
		t.Fatal("entry has no result")
	}
	if strings.Contains(entry.Result.Stderr, "secretdiagnostic") {
		t.Errorf("finding #7: list_requests leaked gated stderr: %q", entry.Result.Stderr)
	}
	if !strings.Contains(entry.Result.Stderr, "stderr gated") {
		t.Errorf("expected stderr gate placeholder, got %q", entry.Result.Stderr)
	}
}
