package integration

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The approver token separates "may submit and read requests" from "may decide
// them". Without it, every credential that authenticates on the web port --
// including the tokens agents hold for the MCP port -- can approve, deny,
// release and whitelist, so an agent could approve its own request.

const approverToken = "approver-token-for-ci"

// approvalMutation is one call that changes approval state.
type approvalMutation struct {
	name   string
	method string
	path   string
	body   interface{}
}

// submitPending files one request_command_for_relay as the given MCP client
// and returns its id. The request stays pending until someone decides it.
func submitPending(t *testing.T, c *MCPClient, id int, word string) string {
	t.Helper()
	resp := c.Call(t, id, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{word},
			"reason":  "approver token test",
		},
	})
	return extractRequestID(t, resp)
}

func initMCP(t *testing.T, c *MCPClient) {
	t.Helper()
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "approver-test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)
}

// webDo sends one authenticated call with no Origin header (the shape of a
// non-browser client such as an agent's curl).
func webDo(t *testing.T, method, url, token string, payload interface{}) (int, string) {
	t.Helper()
	switch method {
	case http.MethodPost:
		code, body := WebPost(t, url, token, payload)
		return code, string(body)
	case http.MethodDelete:
		code, body := WebDelete(t, url, token)
		return code, string(body)
	case http.MethodGet:
		code, body := WebGet(t, url, token)
		return code, string(body)
	}
	t.Fatalf("unsupported method %s", method)
	return 0, ""
}

func requestStatus(t *testing.T, s *TestServer, id string) (string, bool) {
	t.Helper()
	code, body := WebGet(t, s.WebURL()+"/api/requests", approverToken)
	if code != http.StatusOK {
		// Legacy servers do not know the approver token; fall back to master.
		code, body = WebGet(t, s.WebURL()+"/api/requests", testToken)
	}
	if code != http.StatusOK {
		t.Fatalf("list requests: status %d, body %s", code, body)
	}
	var list []RequestResult
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	for _, r := range list {
		if r.ID == id {
			return r.Status, r.OutputGated
		}
	}
	t.Fatalf("request %s not in list", id)
	return "", false
}

func waitForStatus(t *testing.T, s *TestServer, id string, want ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := requestStatus(t, s, id)
		for _, w := range want {
			if st == w {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	st, _ := requestStatus(t, s, id)
	t.Fatalf("request %s stayed %s, want one of %v", id, st, want)
}

// gatedCompleteRequest returns the id of a request that ran with its output
// gated, so /release has something to act on.
func gatedCompleteRequest(t *testing.T, s *TestServer, c *MCPClient, id int) string {
	t.Helper()
	rid := submitPending(t, c, id, "gated")
	code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve-gated", s.WebURL(), rid), approverToken, nil)
	if code != http.StatusOK {
		t.Fatalf("approver approve-gated: status %d, body %s", code, body)
	}
	waitForStatus(t, s, rid, "complete", "error")
	if _, gated := requestStatus(t, s, rid); !gated {
		t.Fatalf("request %s is not output-gated after approve-gated", rid)
	}
	return rid
}

// agentMutations lists every approval-state mutation, aimed at request rid
// (pending) and gatedID (complete, output gated).
func agentMutations(rid, gatedID string) []approvalMutation {
	return []approvalMutation{
		{"approve", http.MethodPost, "/api/requests/" + rid + "/approve", nil},
		{"approve-gated", http.MethodPost, "/api/requests/" + rid + "/approve-gated", nil},
		{"deny", http.MethodPost, "/api/requests/" + rid + "/deny", map[string]string{"reason": "self-deny"}},
		{"release", http.MethodPost, "/api/requests/" + gatedID + "/release", nil},
		{"whitelist-add", http.MethodPost, "/api/whitelist", map[string]interface{}{"request_id": rid}},
		{"whitelist-add-raw", http.MethodPost, "/api/whitelist", map[string]interface{}{"command": "echo", "args": []string{"x"}}},
		{"whitelist-remove", http.MethodPost, "/api/whitelist/remove", map[string]interface{}{"command": "echo", "args": []string{"seeded"}}},
		{"turbocharge-on", http.MethodPost, "/api/turbocharge", map[string]int{"duration_minutes": 5, "cooldown_seconds": 1}},
		{"turbocharge-off", http.MethodDelete, "/api/turbocharge", nil},
	}
}

// TestApproverToken_AgentTokensCannotMutateApprovalState is the hole itself:
// with the approver token configured, neither the master token nor a minted
// per-client token may change approval state, and the request stays pending.
func TestApproverToken_AgentTokensCannotMutateApprovalState(t *testing.T) {
	dataDir := t.TempDir()
	clientToken := mintClient(t, dataDir, "agent-a")
	s := StartServer(t, WithDataDir(dataDir), WithApproverToken(approverToken))

	c := NewMCPClientWithToken(t, s.MCPURL(), clientToken)
	initMCP(t, c)
	rid := submitPending(t, c, 2, "self-approve")
	gatedID := gatedCompleteRequest(t, s, c, 3)

	// Seed a rule so whitelist-remove has a real target to refuse.
	if code, body := WebPost(t, s.WebURL()+"/api/whitelist", approverToken,
		map[string]interface{}{"command": "echo", "args": []string{"seeded"}}); code != http.StatusOK {
		t.Fatalf("seed whitelist rule: status %d, body %s", code, body)
	}

	for _, tok := range []struct{ name, token string }{{"master", testToken}, {"per-client", clientToken}} {
		for _, m := range agentMutations(rid, gatedID) {
			t.Run(tok.name+"/"+m.name, func(t *testing.T) {
				code, body := webDo(t, m.method, s.WebURL()+m.path, tok.token, m.body)
				if code != http.StatusForbidden {
					t.Fatalf("%s with %s token: status %d, body %q, want 403", m.name, tok.name, code, body)
				}
				if !strings.Contains(body, "cannot approve") {
					t.Fatalf("%s 403 body = %q, want it to say the token cannot approve", m.name, body)
				}
			})
		}
	}

	if st, _ := requestStatus(t, s, rid); st != "pending" {
		t.Fatalf("request %s is %s after refused agent calls, want pending", rid, st)
	}
	if _, gated := requestStatus(t, s, gatedID); !gated {
		t.Fatalf("request %s output was released by an agent token", gatedID)
	}
	_, wl := WebGet(t, s.WebURL()+"/api/whitelist", approverToken)
	if strings.Contains(string(wl), `"x"`) {
		t.Fatalf("an agent token added a whitelist rule: %s", wl)
	}
	if !strings.Contains(string(wl), "seeded") {
		t.Fatalf("an agent token removed a whitelist rule: %s", wl)
	}
	code, tb := WebGet(t, s.WebURL()+"/api/turbocharge", approverToken)
	if code != http.StatusOK || strings.Contains(string(tb), `"active":true`) {
		t.Fatalf("turbocharge state changed by an agent token: %d %s", code, tb)
	}
}

// TestApproverToken_ApproverCanMutateApprovalState: the approver token does
// every one of those things.
func TestApproverToken_ApproverCanMutateApprovalState(t *testing.T) {
	s := StartServer(t, WithApproverToken(approverToken))
	c := NewMCPClient(t, s.MCPURL())
	initMCP(t, c)

	approveID := submitPending(t, c, 2, "approve-me")
	denyID := submitPending(t, c, 3, "deny-me")
	gatedID := gatedCompleteRequest(t, s, c, 4)

	steps := []approvalMutation{
		{"approve", http.MethodPost, "/api/requests/" + approveID + "/approve", nil},
		{"deny", http.MethodPost, "/api/requests/" + denyID + "/deny", map[string]string{"reason": "no"}},
		{"release", http.MethodPost, "/api/requests/" + gatedID + "/release", nil},
		{"whitelist-add", http.MethodPost, "/api/whitelist", map[string]interface{}{"command": "echo", "args": []string{"y"}}},
		{"whitelist-remove", http.MethodPost, "/api/whitelist/remove", map[string]interface{}{"command": "echo", "args": []string{"y"}}},
		{"turbocharge-on", http.MethodPost, "/api/turbocharge", map[string]int{"duration_minutes": 5, "cooldown_seconds": 1}},
		{"turbocharge-off", http.MethodDelete, "/api/turbocharge", nil},
	}
	for _, m := range steps {
		code, body := webDo(t, m.method, s.WebURL()+m.path, approverToken, m.body)
		if code != http.StatusOK {
			t.Fatalf("approver %s: status %d, body %q, want 200", m.name, code, body)
		}
	}
	waitForStatus(t, s, approveID, "complete")
	if st, _ := requestStatus(t, s, denyID); st != "denied" {
		t.Fatalf("deny: request is %s, want denied", st)
	}
	if _, gated := requestStatus(t, s, gatedID); gated {
		t.Fatal("release: request is still output-gated")
	}
}

// TestApproverToken_ReadsAcceptEitherToken: reads stay open to every token, so
// agents that watch the queue (list, content, whitelist, turbo state, /events)
// keep working.
func TestApproverToken_ReadsAcceptEitherToken(t *testing.T) {
	dataDir := t.TempDir()
	clientToken := mintClient(t, dataDir, "agent-b")
	s := StartServer(t, WithDataDir(dataDir), WithApproverToken(approverToken))
	c := NewMCPClient(t, s.MCPURL())
	initMCP(t, c)
	rid := submitPending(t, c, 2, "read-me")

	for _, tok := range []struct{ name, token string }{
		{"master", testToken}, {"per-client", clientToken}, {"approver", approverToken},
	} {
		for _, path := range []string{
			"/api/requests",
			"/api/requests?status=pending",
			"/api/requests/" + rid + "/content",
			"/api/whitelist",
			"/api/turbocharge",
		} {
			if code, body := WebGet(t, s.WebURL()+path, tok.token); code != http.StatusOK {
				t.Errorf("%s GET %s: status %d, body %s, want 200", tok.name, path, code, body)
			}
		}

		resp := WebGetResp(t, s.WebURL()+"/events", tok.token)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("%s GET /events: status %d, want 200", tok.name, resp.StatusCode)
		}
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		resp.Body.Close()
		if err != nil || !strings.HasPrefix(line, ": connected") {
			t.Fatalf("%s /events first line = %q (err %v), want the connected comment", tok.name, line, err)
		}
	}
}

// TestApproverToken_AgentWritesThatAreNotDecisionsStillWork: an agent's own
// submissions over the web port -- the permission-check gate -- are not
// approval decisions and keep working with an agent token.
func TestApproverToken_AgentWritesThatAreNotDecisionsStillWork(t *testing.T) {
	s := StartServer(t, WithApproverToken(approverToken), WithPermissionsFile(writePermFile(t)))
	code, body := WebPost(t, s.WebURL()+"/api/permission/check", testToken, map[string]any{
		"tool":  "Bash",
		"input": map[string]any{"command": "git push origin main"},
	})
	if code != http.StatusOK {
		t.Fatalf("agent permission check: status %d, body %s, want 200", code, body)
	}
}

// TestApproverToken_NotAcceptedOnMCPPort: the approver credential belongs to
// the dashboard only. It is not an agent credential, so the MCP port refuses it.
func TestApproverToken_NotAcceptedOnMCPPort(t *testing.T) {
	s := StartServer(t, WithApproverToken(approverToken))
	req, _ := http.NewRequest(http.MethodGet, s.MCPURL()+"/sse", nil)
	req.Header.Set("Authorization", "Bearer "+approverToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("approver token on the MCP port: status %d, want 401", resp.StatusCode)
	}
}

// TestApproverToken_UnsetKeepsLegacyBehaviour: a deployment that has not set
// the approver token keeps working exactly as before, and says so at startup.
func TestApproverToken_UnsetKeepsLegacyBehaviour(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())
	initMCP(t, c)
	rid := submitPending(t, c, 2, "legacy")

	if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), rid), testToken, nil); code != http.StatusOK {
		t.Fatalf("legacy approve with the shared token: status %d, body %s", code, body)
	}
	if code, body := WebPost(t, s.WebURL()+"/api/turbocharge", testToken, map[string]int{"duration_minutes": 1}); code != http.StatusOK {
		t.Fatalf("legacy turbocharge with the shared token: status %d, body %s", code, body)
	}
	if !strings.Contains(s.Stderr(), "MHR_APPROVER_TOKEN is not set") {
		t.Fatalf("no startup warning about the missing approver token; stderr:\n%s", s.Stderr())
	}
}

// TestApproverToken_AuditRecordsApprovedBy: every decision in the audit log
// says which kind of credential made it.
func TestApproverToken_AuditRecordsApprovedBy(t *testing.T) {
	cases := []struct {
		name  string
		opts  []ServerOption
		token string
		want  string
	}{
		{"approver", []ServerOption{WithApproverToken(approverToken)}, approverToken, "approver"},
		{"legacy", nil, testToken, "legacy-shared-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			s := StartServer(t, append([]ServerOption{WithDataDir(dataDir)}, tc.opts...)...)
			c := NewMCPClient(t, s.MCPURL())
			initMCP(t, c)
			approveID := submitPending(t, c, 2, "audit-approve")
			denyID := submitPending(t, c, 3, "audit-deny")

			if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), approveID), tc.token, nil); code != http.StatusOK {
				t.Fatalf("approve: %d %s", code, body)
			}
			if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), denyID), tc.token, map[string]string{"reason": "r"}); code != http.StatusOK {
				t.Fatalf("deny: %d %s", code, body)
			}
			waitForStatus(t, s, approveID, "complete")

			got := map[string]string{}
			for _, e := range readAuditLog(t, filepath.Join(dataDir, "audit.log")) {
				switch e.Event {
				case "request_approved", "request_denied":
					by, _ := e.Fields["approved_by"].(string)
					got[e.Event] = by
				}
			}
			for _, ev := range []string{"request_approved", "request_denied"} {
				if got[ev] != tc.want {
					t.Errorf("%s approved_by = %q, want %q", ev, got[ev], tc.want)
				}
			}
		})
	}
}

// TestApproverToken_MustDifferFromAgentToken: an approver token equal to any
// agent token (the shared one or a minted client) would re-open the hole, so
// the relay refuses to start.
func TestApproverToken_MustDifferFromAgentToken(t *testing.T) {
	t.Run("auth-token", func(t *testing.T) {
		assertRefusesApprover(t, t.TempDir(), testToken, "must differ from MHR_AUTH_TOKEN")
	})
	t.Run("client-token", func(t *testing.T) {
		dataDir := t.TempDir()
		assertRefusesApprover(t, dataDir, mintClient(t, dataDir, "agent-c"), "must differ from every client token")
	})
}

func assertRefusesApprover(t *testing.T, dataDir, approver, wantMsg string) {
	t.Helper()
	assertRefusesStart(t, dataDir, []string{"MHR_APPROVER_TOKEN=" + approver}, wantMsg)
}

func assertRefusesStart(t *testing.T, dataDir string, extraEnv []string, wantMsg string) {
	t.Helper()
	bin := os.Getenv("HUMAN_RELAY_BIN")
	if bin == "" {
		t.Fatal("HUMAN_RELAY_BIN not set")
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"MHR_AUTH_TOKEN="+testToken,
		"MHR_DATA_DIR="+dataDir,
		"MHR_MCP_PORT=0",
		"MHR_WEB_PORT=0",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("relay started with an approver token an agent holds; want it to refuse")
	}
	if err == nil {
		t.Fatalf("relay exited 0 with a reused approver token; output:\n%s", out)
	}
	if !strings.Contains(string(out), wantMsg) {
		t.Fatalf("refusal does not name the problem; output:\n%s", out)
	}
}

// runApproved submits one relay-local command as the agent, approves it with
// the approver token, and returns its stdout.
func runApproved(t *testing.T, s *TestServer, c *MCPClient, id int, command string, args ...string) string {
	t.Helper()
	resp := c.Call(t, id, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": command,
			"args":    args,
			"reason":  "approver token test",
		},
	})
	rid := extractRequestID(t, resp)
	if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), rid), approverToken, nil); code != http.StatusOK {
		t.Fatalf("approve %s: %d %s", command, code, body)
	}
	waitForStatus(t, s, rid, "complete", "error")
	res := extractResult(t, c.Call(t, id+1000, "tools/call", map[string]interface{}{
		"name":      "get_result",
		"arguments": map[string]interface{}{"request_id": rid},
	}))
	if res.Result == nil {
		t.Fatalf("no result for %s", rid)
	}
	return res.Result.Stdout
}

// TestApproverToken_NotInheritedByApprovedCommands: commands the relay runs
// inherit its environment, so an approved script that prints `env` would hand
// an agent the approver token. The relay drops it from its environment once
// read.
func TestApproverToken_NotInheritedByApprovedCommands(t *testing.T) {
	s := StartServer(t, WithApproverToken(approverToken))
	c := NewMCPClient(t, s.MCPURL())
	initMCP(t, c)
	out := runApproved(t, s, c, 2, "env")
	if !strings.Contains(out, "MHR_AUTH_TOKEN") {
		t.Fatal("control failed: env output does not show the relay's environment")
	}
	if strings.Contains(out, approverToken) || strings.Contains(out, "MHR_APPROVER_TOKEN") {
		// Never print `out`: it is the whole test environment.
		t.Fatal("an approved command can read the approver token from its environment")
	}
}

// TestApproverToken_NotInRelayInitialEnviron: os.Unsetenv only edits Go's copy
// of the environment. The kernel keeps the block the process was exec'd with
// and serves it at /proc/<pid>/environ, which any command the relay runs (same
// uid) can read. The plaintext form must not survive there either.
func TestApproverToken_NotInRelayInitialEnviron(t *testing.T) {
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc on this platform")
	}
	s := StartServer(t, WithApproverToken(approverToken))
	c := NewMCPClient(t, s.MCPURL())
	initMCP(t, c)
	environPath := fmt.Sprintf("/proc/%d/environ", s.cmd.Process.Pid)
	out := runApproved(t, s, c, 2, "sh", "-c", "tr '\\0' '\\n' < "+environPath)
	if !strings.Contains(out, "MHR_AUTH_TOKEN=") {
		t.Fatal("control failed: the relay's /proc environ is not readable, so this test measures nothing")
	}
	if strings.Contains(out, approverToken) {
		// Never print `out`: it is the relay's whole environment.
		t.Fatal("the plaintext approver token is still in the relay's /proc/<pid>/environ")
	}
	// The relay still enforces the approver after dropping the plaintext.
	rid := submitPending(t, c, 3, "after-reexec")
	if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), rid), testToken, nil); code != http.StatusForbidden {
		t.Fatalf("agent approve after the environ scrub: %d %s, want 403", code, body)
	}
}

// TestApproverToken_DigestForm: MHR_APPROVER_TOKEN_SHA256 configures the
// approver by the token's SHA-256 alone, so the plaintext never has to exist
// on the relay host at all.
func TestApproverToken_DigestForm(t *testing.T) {
	sum := sha256.Sum256([]byte(approverToken))
	s := StartServer(t, WithApproverTokenSHA256(hex.EncodeToString(sum[:])))
	c := NewMCPClient(t, s.MCPURL())
	initMCP(t, c)
	rid := submitPending(t, c, 2, "digest")

	if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), rid), testToken, nil); code != http.StatusForbidden {
		t.Fatalf("agent approve under the digest form: %d %s, want 403", code, body)
	}
	if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), rid), approverToken, nil); code != http.StatusOK {
		t.Fatalf("approver approve under the digest form: %d %s, want 200", code, body)
	}
}

// TestApproverToken_BadConfigRefused: the digest form is checked as strictly
// as the plaintext form.
func TestApproverToken_BadConfigRefused(t *testing.T) {
	authSum := sha256.Sum256([]byte(testToken))
	cases := []struct {
		name, env, want string
	}{
		{"both-forms", "MHR_APPROVER_TOKEN=x\x00MHR_APPROVER_TOKEN_SHA256=" + strings.Repeat("a", 64), "not both"},
		{"malformed-digest", "MHR_APPROVER_TOKEN_SHA256=not-hex", "64 hex characters"},
		{"digest-of-auth-token", "MHR_APPROVER_TOKEN_SHA256=" + hex.EncodeToString(authSum[:]), "must differ from MHR_AUTH_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusesStart(t, t.TempDir(), strings.Split(tc.env, "\x00"), tc.want)
		})
	}
}

// approverGateOn asserts the approver gate is on: an agent token is refused a
// decision and the approver token may make it.
func approverGateOn(t *testing.T, s *TestServer) {
	t.Helper()
	turbo := map[string]int{"duration_minutes": 1, "cooldown_seconds": 1}
	if code, body := WebPost(t, s.WebURL()+"/api/turbocharge", testToken, turbo); code != http.StatusForbidden {
		t.Fatalf("agent-token turbocharge: status %d, body %s, want 403 -- the approver gate is OFF and any agent token can approve", code, body)
	}
	if code, body := WebPost(t, s.WebURL()+"/api/turbocharge", approverToken, turbo); code != http.StatusOK {
		t.Fatalf("approver turbocharge: status %d, body %s, want 200", code, body)
	}
	if strings.Contains(s.Stderr(), "MHR_APPROVER_TOKEN is not set") {
		t.Fatalf("relay logged the legacy-mode warning although an approver was configured; stderr:\n%s", s.Stderr())
	}
}

// TestApproverToken_EmptyDigestVarDoesNotDisableGate: an env file that lists
// both keys and leaves MHR_APPROVER_TOKEN_SHA256= empty is ordinary. The
// re-exec that swaps the plaintext for its digest once kept that empty entry
// ahead of the digest it appended; Go reads the first entry, so the relay
// started in legacy mode where every agent token approves.
func TestApproverToken_EmptyDigestVarDoesNotDisableGate(t *testing.T) {
	s := StartServer(t, WithApproverToken(approverToken), WithApproverTokenSHA256(""))
	approverGateOn(t, s)

	if _, err := os.Stat("/proc/self/environ"); err != nil {
		return
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", s.cmd.Process.Pid))
	if err != nil {
		t.Fatalf("read relay environ: %v", err)
	}
	var digestEntries, emptyEntries int
	for _, kv := range strings.Split(string(raw), "\x00") {
		if strings.HasPrefix(kv, "MHR_APPROVER_TOKEN_SHA256=") {
			digestEntries++
			if kv == "MHR_APPROVER_TOKEN_SHA256=" {
				emptyEntries++
			}
		}
	}
	if digestEntries != 1 || emptyEntries != 0 {
		t.Fatalf("relay environ holds %d MHR_APPROVER_TOKEN_SHA256 entries (%d empty), want exactly 1 non-empty", digestEntries, emptyEntries)
	}
	if strings.Contains(string(raw), approverToken) {
		t.Fatal("the plaintext approver token is in the relay's /proc/<pid>/environ")
	}
}

// TestApproverToken_DigestWithEmptyTokenVar: the SHA-256-only form, from an env
// file that also lists MHR_APPROVER_TOKEN= empty. The empty plaintext variable
// must neither switch the gate off nor count as "both set".
func TestApproverToken_DigestWithEmptyTokenVar(t *testing.T) {
	sum := sha256.Sum256([]byte(approverToken))
	s := StartServer(t, WithApproverToken(""), WithApproverTokenSHA256(hex.EncodeToString(sum[:])))
	approverGateOn(t, s)
}

// TestApproverToken_BothEmptyKeepsLegacyBehaviour: both variables present and
// empty is "not configured", the same as both absent.
func TestApproverToken_BothEmptyKeepsLegacyBehaviour(t *testing.T) {
	s := StartServer(t, WithApproverToken(""), WithApproverTokenSHA256(""))
	if code, body := WebPost(t, s.WebURL()+"/api/turbocharge", testToken, map[string]int{"duration_minutes": 1}); code != http.StatusOK {
		t.Fatalf("legacy turbocharge with the shared token: status %d, body %s", code, body)
	}
	if !strings.Contains(s.Stderr(), "MHR_APPROVER_TOKEN is not set") {
		t.Fatalf("no startup warning about the missing approver token; stderr:\n%s", s.Stderr())
	}
}

// TestApproverToken_DuplicateEntryRefused: an environment block with two
// entries for one approver variable is ambiguous -- Go's Getenv takes the
// first, other readers the last -- and with an empty entry first it read as
// "no approver", i.e. legacy mode. The relay must refuse to start instead.
//
// os/exec de-duplicates Cmd.Env (keeping the last entry), which would hide the
// bug, so these start the binary with os.StartProcess and the block verbatim.
func TestApproverToken_DuplicateEntryRefused(t *testing.T) {
	sum := sha256.Sum256([]byte(approverToken))
	digest := hex.EncodeToString(sum[:])
	cases := []struct{ name, first, second, want string }{
		{"digest-empty-then-set", "MHR_APPROVER_TOKEN_SHA256=", "MHR_APPROVER_TOKEN_SHA256=" + digest, "MHR_APPROVER_TOKEN_SHA256 appears more than once"},
		{"token-empty-then-set", "MHR_APPROVER_TOKEN=", "MHR_APPROVER_TOKEN=" + approverToken, "MHR_APPROVER_TOKEN appears more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusesStartRawEnv(t, []string{tc.first, tc.second}, tc.want)
		})
	}
}

// TestApproverToken_WhitespaceOnlyRefused: an approver variable whose value is
// only whitespace must stop the relay. Read as a token, it locks the approver
// out for good (the web port trims the presented bearer token, so nobody can
// send it); read as unset, it is legacy mode, where every agent token
// approves. Neither is what the operator meant, so the relay refuses to start.
// The plaintext cases also exercise the re-exec that swaps a plaintext token
// for its digest: it must not launder "   " into a valid-looking digest.
func TestApproverToken_WhitespaceOnlyRefused(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"token-spaces", []string{"MHR_APPROVER_TOKEN=   "}, "MHR_APPROVER_TOKEN is whitespace only"},
		{"token-tab", []string{"MHR_APPROVER_TOKEN=\t"}, "MHR_APPROVER_TOKEN is whitespace only"},
		{"token-spaces-empty-digest", []string{"MHR_APPROVER_TOKEN=  ", "MHR_APPROVER_TOKEN_SHA256="}, "MHR_APPROVER_TOKEN is whitespace only"},
		{"digest-spaces", []string{"MHR_APPROVER_TOKEN_SHA256=   "}, "MHR_APPROVER_TOKEN_SHA256 is whitespace only"},
		{"digest-spaces-empty-token", []string{"MHR_APPROVER_TOKEN=", "MHR_APPROVER_TOKEN_SHA256= "}, "MHR_APPROVER_TOKEN_SHA256 is whitespace only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefusesStartRawEnv(t, tc.env, tc.want)
		})
	}
}

// assertRefusesStartRawEnv starts the relay with os.StartProcess, so the
// environment reaches it exactly as given (duplicates included), and asserts
// it exits non-zero naming wantMsg rather than serving.
func assertRefusesStartRawEnv(t *testing.T, extraEnv []string, wantMsg string) {
	t.Helper()
	bin := os.Getenv("HUMAN_RELAY_BIN")
	if bin == "" {
		t.Fatal("HUMAN_RELAY_BIN not set")
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"MHR_AUTH_TOKEN=" + testToken,
		"MHR_DATA_DIR=" + t.TempDir(),
		"MHR_MCP_PORT=0",
		"MHR_WEB_PORT=0",
	}
	env = append(env, extraEnv...)
	logPath := filepath.Join(t.TempDir(), "relay.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	proc, err := os.StartProcess(bin, []string{bin}, &os.ProcAttr{
		Env:   env,
		Files: []*os.File{nil, logFile, logFile},
	})
	if err != nil {
		t.Fatalf("start relay: %v", err)
	}
	done := make(chan *os.ProcessState, 1)
	go func() { st, _ := proc.Wait(); done <- st }()
	var st *os.ProcessState
	select {
	case st = <-done:
	case <-time.After(5 * time.Second):
		proc.Kill()
		<-done
		out, _ := os.ReadFile(logPath)
		t.Fatalf("relay started with an ambiguous approver environment (%q) instead of refusing; it is serving, possibly in legacy mode where agent tokens approve. log:\n%s", extraEnv, out)
	}
	out, _ := os.ReadFile(logPath)
	if st.Success() {
		t.Fatalf("relay exited 0 with an ambiguous approver environment; log:\n%s", out)
	}
	if !strings.Contains(string(out), wantMsg) {
		t.Fatalf("refusal does not name the problem (want %q); log:\n%s", wantMsg, out)
	}
}
