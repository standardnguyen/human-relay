package integration

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runRelayCLI drives the built relay binary's -client-* subcommands against
// dataDir. It returns stdout, stderr and the exit code.
func runRelayCLI(t *testing.T, dataDir string, args ...string) (string, string, int) {
	t.Helper()
	bin := os.Getenv("HUMAN_RELAY_BIN")
	if bin == "" {
		t.Fatal("HUMAN_RELAY_BIN not set")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "MHR_DATA_DIR="+dataDir)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// mintClient adds a client through the CLI and returns the one-time token.
func mintClient(t *testing.T, dataDir, name string) string {
	t.Helper()
	out, errOut, code := runRelayCLI(t, dataDir, "-client-add", name)
	if code != 0 {
		t.Fatalf("-client-add %s exited %d: %s", name, code, errOut)
	}
	token := strings.TrimSpace(out)
	if token == "" {
		t.Fatal("-client-add printed no token on stdout")
	}
	if len(strings.Fields(token)) != 1 {
		t.Fatalf("-client-add printed more than one line: %q", out)
	}
	return token
}

// TestPerClientTokenAuthenticatesWhereMasterDoes is the core contract: a token
// minted for one consumer works on both ports while the master token keeps
// working for everyone else.
func TestPerClientTokenAuthenticatesWhereMasterDoes(t *testing.T) {
	dataDir := t.TempDir()
	token := mintClient(t, dataDir, "cc-115")

	s := StartServer(t, WithDataDir(dataDir))

	// Master still authenticates (no migration for the existing fleet).
	master := NewMCPClient(t, s.MCPURL())
	master.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "master", "version": "1.0"},
	})
	if resp := master.Call(t, 2, "tools/list", nil); resp.Error != nil {
		t.Fatalf("master tools/list error: %+v", resp.Error)
	}

	// The per-client token authenticates on the MCP port...
	c := NewMCPClientWithToken(t, s.MCPURL(), token)
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "cc-115", "version": "1.0"},
	})
	if resp := c.Call(t, 2, "tools/list", nil); resp.Error != nil {
		t.Fatalf("client tools/list error: %+v", resp.Error)
	}

	// ...and on the web port.
	if code, body := WebGet(t, s.WebURL()+"/api/requests", token); code != 200 {
		t.Fatalf("per-client web GET: status %d, body %s", code, body)
	}
}

// TestRevokedClientTokenRejected: revoking a client invalidates its token on
// both ports without touching anyone else's.
func TestRevokedClientTokenRejected(t *testing.T) {
	dataDir := t.TempDir()
	token := mintClient(t, dataDir, "kapsrh")

	if _, errOut, code := runRelayCLI(t, dataDir, "-client-revoke", "kapsrh"); code != 0 {
		t.Fatalf("-client-revoke exited %d: %s", code, errOut)
	}

	s := StartServer(t, WithDataDir(dataDir))

	if code, body := WebGet(t, s.WebURL()+"/api/requests", token); code != http.StatusUnauthorized {
		t.Fatalf("revoked web GET: status %d, body %s, want 401", code, body)
	}

	req, err := http.NewRequest(http.MethodGet, s.MCPURL()+"/sse", nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoked SSE GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked SSE: status %d, want 401", resp.StatusCode)
	}

	// The master must be unaffected by another client's revocation.
	if code, body := WebGet(t, s.WebURL()+"/api/requests", testToken); code != 200 {
		t.Fatalf("master web GET after revoke: status %d, body %s", code, body)
	}
}

// TestUnknownTokenRejected pins the 401/unauthorized response for a token that
// was never minted.
func TestUnknownTokenRejected(t *testing.T) {
	s := StartServer(t)

	code, body := WebGet(t, s.WebURL()+"/api/requests", "totally-unknown-token")
	if code != http.StatusUnauthorized {
		t.Fatalf("unknown token: status %d, body %s, want 401", code, body)
	}
	if !strings.Contains(string(body), "unauthorized") {
		t.Fatalf("unknown token body = %q, want the bare unauthorized error", body)
	}

	// Same shape for the MCP port.
	req, err := http.NewRequest(http.MethodGet, s.MCPURL()+"/sse", nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer totally-unknown-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unknown-token SSE GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown-token SSE: status %d, want 401", resp.StatusCode)
	}
}

// TestClientNameRecordedOnRequest: the authenticated client's name lands on the
// request it created, and is visible through both list_requests and the web
// dashboard API.
func TestClientNameRecordedOnRequest(t *testing.T) {
	dataDir := t.TempDir()
	token := mintClient(t, dataDir, "cc-115")

	s := StartServer(t, WithDataDir(dataDir))
	c := NewMCPClientWithToken(t, s.MCPURL(), token)
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "cc-115", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	requestID := createRequest(t, c, 2, "client attribution")

	listed := webListRequests(t, s, "")
	var found *RequestResult
	for i := range listed {
		if listed[i].ID == requestID {
			found = &listed[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("request %s not present in /api/requests", requestID)
	}
	if found.Client != "cc-115" {
		t.Fatalf("web request client = %q, want cc-115", found.Client)
	}

	// list_requests must carry the same attribution to the agent.
	master := NewMCPClient(t, s.MCPURL())
	master.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "master", "version": "1.0"},
	})
	resp := master.Call(t, 2, "tools/call", map[string]interface{}{
		"name":      "list_requests",
		"arguments": map[string]interface{}{},
	})
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("parse list_requests result: %v (%s)", err, resp.Result)
	}
	var viaMCP []RequestResult
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &viaMCP); err != nil {
		t.Fatalf("parse list_requests payload: %v", err)
	}
	var mcpClient string
	for _, r := range viaMCP {
		if r.ID == requestID {
			mcpClient = r.Client
		}
	}
	if mcpClient != "cc-115" {
		t.Fatalf("list_requests client = %q, want cc-115", mcpClient)
	}
}

// TestClientAddDuplicateRefusedAndList: -client-add refuses a name it already
// has (printing no token), and -client-list reports the client.
func TestClientAddDuplicateRefusedAndList(t *testing.T) {
	dataDir := t.TempDir()
	token := mintClient(t, dataDir, "cc-115")

	out, _, code := runRelayCLI(t, dataDir, "-client-add", "cc-115")
	if code == 0 {
		t.Fatal("duplicate -client-add exited 0, want non-zero")
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("duplicate -client-add printed %q on stdout, want nothing", out)
	}
	if strings.Contains(out, token) {
		t.Fatal("duplicate -client-add leaked the existing token")
	}

	listOut, listErr, listCode := runRelayCLI(t, dataDir, "-client-list")
	if listCode != 0 {
		t.Fatalf("-client-list exited %d: %s", listCode, listErr)
	}
	if !strings.Contains(listOut, "cc-115") {
		t.Fatalf("-client-list missing cc-115: %q", listOut)
	}
}

// A sanity check that the minted token is not any trivially-guessable string.
func TestMintedTokenIsOpaque(t *testing.T) {
	dataDir := t.TempDir()
	token := mintClient(t, dataDir, "opacity-check")
	if len(token) < 32 {
		t.Fatalf("minted token length %d, want at least 32 chars of entropy", len(token))
	}
	if strings.ContainsAny(token, " /+=") {
		t.Fatalf("minted token %q is not base64url", token)
	}
}
