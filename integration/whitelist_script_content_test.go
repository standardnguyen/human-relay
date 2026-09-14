package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type wlRule struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

func fetchWhitelistRules(t *testing.T, s *TestServer) []wlRule {
	t.Helper()
	code, body := WebGet(t, s.WebURL()+"/api/whitelist", s.token)
	if code != 200 {
		t.Fatalf("GET /api/whitelist returned %d", code)
	}
	var rules []wlRule
	if err := json.Unmarshal(body, &rules); err != nil {
		t.Fatalf("parse whitelist: %v\nraw: %s", err, body)
	}
	return rules
}

// TestWhitelistScriptCreateIsContentKeyed pins finding #8: the whitelist match
// key for script_create / script_create_then_run was the script NAME alone, so
// one human click ("whitelist this create_script") became a standing grant to
// execute ANY future body submitted under that name — a persistent
// no-approval code-execution path.
//
// The key now includes a hash of the body, and both sides of the match derive
// it from the same whitelistKey function: the dashboard's add path sends the
// request id and the server keys the rule off the stored request, so the rule
// and the matcher cannot disagree.
func TestWhitelistScriptCreateIsContentKeyed(t *testing.T) {
	cases := []struct {
		tool        string
		ruleCommand string
		// derived is the ScriptName the tool stores (create_then_run prefixes
		// bare names with oneshot/).
		bareName string
	}{
		{tool: "create_script", ruleCommand: "create_script", bareName: "wl-content-probe"},
		{tool: "create_then_run", ruleCommand: "create_then_run", bareName: "wl-ctr-probe"},
	}

	const reviewedBody = "#!/bin/sh\necho reviewed\n"
	const swappedBody = "#!/bin/sh\necho pwned\n"

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			dir := t.TempDir()
			wlPath := filepath.Join(dir, "whitelist.json")
			os.WriteFile(wlPath, []byte(`[]`), 0644)
			scriptsDir := t.TempDir()

			s := StartServer(t, WithWhitelistFile(wlPath), WithScriptsDir(scriptsDir))
			c := NewMCPClient(t, s.MCPURL())

			c.Call(t, 1, "initialize", map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{},
				"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
			})
			c.Notify(t, "notifications/initialized", nil)

			submit := func(callID int, content string) string {
				t.Helper()
				resp := c.Call(t, callID, "tools/call", map[string]interface{}{
					"name": tc.tool,
					"arguments": map[string]interface{}{
						"name":    tc.bareName,
						"content": content,
						"reason":  "content-keyed whitelist test",
					},
				})
				return extractRequestID(t, resp)
			}
			get := func(callID int, id string) *RequestResult {
				t.Helper()
				resp := c.Call(t, callID, "tools/call", map[string]interface{}{
					"name": "get_result",
					"arguments": map[string]interface{}{
						"request_id": id,
					},
				})
				return extractResult(t, resp)
			}

			// The operator reviews one body and whitelists it, exactly as the
			// dashboard's Whitelist button does (request id, not a key the
			// browser assembled — it never sees the body hash).
			reviewedID := submit(2, reviewedBody)
			reviewed := get(3, reviewedID)
			if reviewed.StdinSHA256 == "" {
				t.Fatal("request exposes no stdin_sha256 — the dashboard cannot tell what is whitelisted")
			}

			// command/args here are the weak, name-only key a client can still
			// assemble (and the pre-fix dashboard did). With a request_id
			// present the server must derive the key from the stored request
			// and ignore what the client claimed — otherwise the rule-creation
			// side drifts from the matching side and the grant stays name-wide.
			code, body := WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
				"request_id": reviewedID,
				"command":    tc.ruleCommand,
				"args":       []string{tc.bareName},
			})
			if code != 200 {
				t.Fatalf("whitelist add returned %d: %s", code, body)
			}

			rules := fetchWhitelistRules(t, s)
			if len(rules) != 1 {
				t.Fatalf("expected 1 rule, got %d", len(rules))
			}
			if rules[0].Command != tc.ruleCommand {
				t.Errorf("rule command = %q, want %q", rules[0].Command, tc.ruleCommand)
			}
			if len(rules[0].Args) != 2 {
				t.Fatalf("finding #8: rule args = %v, want [name, content-hash]", rules[0].Args)
			}
			if rules[0].Args[0] != reviewed.ScriptName {
				t.Errorf("rule name arg = %q, want %q", rules[0].Args[0], reviewed.ScriptName)
			}
			if rules[0].Args[1] != reviewed.StdinSHA256 {
				t.Errorf("rule hash arg = %q, want the request's stdin_sha256 %q",
					rules[0].Args[1], reviewed.StdinSHA256)
			}

			// A DIFFERENT body under the SAME name must not inherit the grant.
			swappedID := submit(4, swappedBody)
			time.Sleep(750 * time.Millisecond)
			swapped := get(5, swappedID)
			if swapped.Status != "pending" {
				t.Fatalf("finding #8: a swapped script body auto-approved under the reviewed name (status %s) — a human must review the new content",
					swapped.Status)
			}
			if swapped.StdinSHA256 == reviewed.StdinSHA256 {
				t.Fatal("test is broken: the two bodies hash the same")
			}

			// Control: the SAME body under the same name still auto-approves,
			// so the rule is content-aware rather than simply dead.
			sameID := submit(6, reviewedBody)
			same := pollUntilDone(t, c, 7, sameID)
			if same.Status != "complete" {
				t.Fatalf("re-submitting the reviewed body did not auto-approve (status %s) — the two sides of the key have drifted apart",
					same.Status)
			}

			// And the swapped body is still sitting in the queue untouched.
			if got := get(8, swappedID); got.Status != "pending" {
				t.Errorf("swapped request status = %s, want it still pending", got.Status)
			}

			// Nothing the operator did not review reached disk.
			onDisk, err := os.ReadFile(filepath.Join(scriptsDir, reviewed.ScriptName+".sh"))
			if err != nil {
				t.Fatalf("reviewed script was not written: %v", err)
			}
			if string(onDisk) != reviewedBody {
				t.Errorf("script on disk = %q, want the reviewed body %q", onDisk, reviewedBody)
			}
		})
	}
}

// TestWhitelistAddWithoutRequestIDStillWorks keeps the rule-management paths
// that operate on an existing rule rather than a request (the gate toggle and
// direct API use) working: no request_id means the posted command/args are the
// key, as before.
func TestWhitelistAddWithoutRequestIDStillWorks(t *testing.T) {
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	os.WriteFile(wlPath, []byte(`[]`), 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))

	code, _ := WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
		"command": "create_script",
		"args":    []string{"legacy-name", "deadbeef"},
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	rules := fetchWhitelistRules(t, s)
	if len(rules) != 1 || len(rules[0].Args) != 2 || rules[0].Args[1] != "deadbeef" {
		t.Fatalf("expected the posted key to be stored verbatim, got %+v", rules)
	}
}

// TestWhitelistAddUnknownRequestIDRejected: a rule must never be recorded from
// a request the server cannot read, since the key would silently fall back to
// whatever the client claimed.
func TestWhitelistAddUnknownRequestIDRejected(t *testing.T) {
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	os.WriteFile(wlPath, []byte(`[]`), 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))

	code, _ := WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
		"request_id": "no-such-request",
		"command":    "create_script",
		"args":       []string{"attacker-chosen"},
	})
	if code != 404 {
		t.Fatalf("expected 404 for unknown request_id, got %d", code)
	}
	if rules := fetchWhitelistRules(t, s); len(rules) != 0 {
		t.Fatalf("expected no rule recorded, got %+v", rules)
	}
}
