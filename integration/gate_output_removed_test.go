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

// The gate_output whitelist flag ("auto-approve, but withhold the output") was
// removed. A relay that still has such a rule on disk must not quietly turn it
// into a plain auto-approve rule, which would hand the agent output it was
// never meant to see. These tests pin the fail-closed paths end to end.

// TestPersistedGateOutputRuleWaitsForManualApproval: a whitelist.json saved
// with a gate_output rule still starts the relay, but that rule no longer
// auto-approves anything; the request waits in the queue like any other.
func TestPersistedGateOutputRuleWaitsForManualApproval(t *testing.T) {
	wlPath := filepath.Join(t.TempDir(), "whitelist.json")
	rules, _ := json.Marshal([]map[string]interface{}{
		{"command": "echo", "args": []string{"was-gated"}, "gate_output": true},
		{"command": "echo", "args": []string{"plain-rule"}},
	})
	if err := os.WriteFile(wlPath, rules, 0644); err != nil {
		t.Fatal(err)
	}

	s, c := initClient(t, WithWhitelistFile(wlPath))

	// Control: the ungated rule in the same file still auto-approves, so a
	// pending result below means the gated rule was skipped, not that the
	// whitelist failed to load.
	ctlID := extractRequestID(t, c.Call(t, 10, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "echo", "args": []string{"plain-rule"}, "reason": "control",
		},
	}))
	if got := pollUntilDone(t, c, 11, ctlID); got.Status != "complete" {
		t.Fatalf("control: ungated whitelist rule did not auto-approve, status %s", got.Status)
	}

	id := extractRequestID(t, c.Call(t, 12, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "echo", "args": []string{"was-gated"}, "reason": "formerly gated rule",
		},
	}))
	time.Sleep(1 * time.Second)
	if st, _ := requestStatus(t, s, id); st != "pending" {
		t.Fatalf("a request matching a persisted gate_output rule is %q, want pending (manual approval)", st)
	}
	named := false
	for _, line := range strings.Split(s.Stderr(), "\n") {
		if strings.Contains(line, "gate_output") && strings.Contains(line, "echo") {
			named = true
		}
	}
	if !named {
		t.Errorf("no startup log line names the skipped gate_output rule; stderr:\n%s", s.Stderr())
	}

	// A human can still approve it by hand.
	if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id), s.token, nil); code != 200 {
		t.Fatalf("manual approve: %d %s", code, body)
	}
	if got := pollUntilDone(t, c, 13, id); got.Status != "complete" {
		t.Fatalf("after manual approve: status %s", got.Status)
	}
}

// TestWhitelistAPIRejectsGateOutput: an add that asks for gate_output is
// refused, rather than stored as an ungated rule that shows the agent its
// output.
func TestWhitelistAPIRejectsGateOutput(t *testing.T) {
	wlPath := filepath.Join(t.TempDir(), "whitelist.json")
	os.WriteFile(wlPath, []byte(`[]`), 0644)
	s := StartServer(t, WithWhitelistFile(wlPath))

	code, body := WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
		"command":     "run_script",
		"args":        []string{"signal-read"},
		"gate_output": true,
	})
	if code != 400 {
		t.Fatalf("POST /api/whitelist with gate_output: status %d, want 400; body %s", code, body)
	}
	if !strings.Contains(string(body), "gate_output") {
		t.Errorf("the 400 does not say why: %s", body)
	}
	_, list := WebGet(t, s.WebURL()+"/api/whitelist", s.token)
	var got []map[string]interface{}
	json.Unmarshal(list, &got)
	if len(got) != 0 {
		t.Fatalf("a refused add still stored %d rule(s): %s", len(got), list)
	}

	// Control: the same add without the flag is accepted.
	code, body = WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
		"command": "run_script",
		"args":    []string{"signal-read"},
	})
	if code != 200 {
		t.Fatalf("control: plain whitelist add returned %d: %s", code, body)
	}
}

// TestManualGatedEmptyOutputAutoReleases: gating protects content and an
// empty result has none, so a gated run with no output is released without
// asking the approver to release nothing. (Formerly exercised through a
// gate_output whitelist rule; the behaviour belongs to manual gating.)
func TestManualGatedEmptyOutputAutoReleases(t *testing.T) {
	s, c := initClient(t)
	id := extractRequestID(t, c.Call(t, 10, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "true", "args": []string{}, "reason": "empty-output auto-release",
		},
	}))
	if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve-gated", s.WebURL(), id), s.token, nil); code != 200 {
		t.Fatalf("approve-gated: %d %s", code, body)
	}
	result := pollUntilDone(t, c, 11, id)
	if result.Status != "complete" {
		t.Fatalf("expected command to complete, got %s", result.Status)
	}
	if result.OutputGated {
		t.Error("expected empty output to auto-release the gate")
	}
	if result.Result == nil || result.Result.Stdout != "" {
		t.Errorf("expected empty stdout, got %+v", result.Result)
	}
}
