package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pollUntilDone polls get_result until the request leaves pending/running or
// the deadline passes, returning the last seen result.
func pollUntilDone(t *testing.T, c *MCPClient, callID int, requestID string) *RequestResult {
	t.Helper()
	var result *RequestResult
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := c.Call(t, callID, "tools/call", map[string]interface{}{
			"name": "get_result",
			"arguments": map[string]interface{}{
				"request_id": requestID,
			},
		})
		result = extractResult(t, resp)
		if result.Status == "complete" || result.Status == "error" {
			return result
		}
		time.Sleep(200 * time.Millisecond)
	}
	return result
}

func TestWhitelistGateOutputAutoApprovesButGates(t *testing.T) {
	// "Whitelist but gate outputs": rule auto-approves execution, output
	// stays gated until the human releases it.
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "echo", "args": []string{"secret"}, "gate_output": true},
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
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"secret"},
			"reason":  "test gated whitelist",
		},
	})
	requestID := extractRequestID(t, resp)

	// Auto-approves and completes without human action...
	result := pollUntilDone(t, c, 3, requestID)
	if result.Status != "complete" {
		t.Fatalf("expected gated-whitelisted command to complete, got %s", result.Status)
	}
	// ...but the agent-visible output is the gate placeholder, not the content.
	if !result.OutputGated {
		t.Error("expected output_gated true on gated-whitelist result")
	}
	if result.Result == nil || !strings.Contains(result.Result.Stdout, "output gated by operator") {
		t.Errorf("expected gate placeholder stdout, got %q", result.Result.Stdout)
	}
	if result.Result != nil && strings.Contains(result.Result.Stdout, "secret") {
		t.Error("gated stdout leaked the real content")
	}

	// Human releases → agent re-polls → real output.
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/release", s.WebURL(), requestID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("release returned status %d", code)
	}

	resp = c.Call(t, 4, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})
	result = extractResult(t, resp)
	if result.OutputGated {
		t.Error("expected output_gated false after release")
	}
	if result.Result == nil || result.Result.Stdout != "secret\n" {
		t.Errorf("expected real stdout after release, got %q", result.Result.Stdout)
	}
}

func TestWhitelistGateOutputEmptyAutoReleases(t *testing.T) {
	// Gating protects content; an empty result has none. A gated-whitelist
	// run with no output auto-releases so the human isn't asked to release
	// nothing (the no-new-messages poll case).
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "true", "args": []string{}, "gate_output": true},
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
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "true",
			"args":    []string{},
			"reason":  "test empty-output auto-release",
		},
	})
	requestID := extractRequestID(t, resp)

	result := pollUntilDone(t, c, 3, requestID)
	if result.Status != "complete" {
		t.Fatalf("expected command to complete, got %s", result.Status)
	}
	if result.OutputGated {
		t.Error("expected empty output to auto-release the gate")
	}
	if result.Result == nil || result.Result.Stdout != "" {
		t.Errorf("expected empty stdout, got %q", result.Result.Stdout)
	}
}

func TestWhitelistAPIAddWithGateOutput(t *testing.T) {
	// /api/whitelist accepts gate_output and it round-trips through the list.
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	os.WriteFile(wlPath, []byte(`[]`), 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))

	code, _ := WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
		"command":     "run_script",
		"args":        []string{"signal-read"},
		"gate_output": true,
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	_, body := WebGet(t, s.WebURL()+"/api/whitelist", s.token)
	var got []map[string]interface{}
	json.Unmarshal(body, &got)
	if len(got) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(got))
	}
	if got[0]["gate_output"] != true {
		t.Errorf("expected gate_output true in listed rule, got %v", got[0]["gate_output"])
	}
}
