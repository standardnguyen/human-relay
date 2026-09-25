package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Gated output is the approver's to release. Every path that can hand a
// request's result to a caller must withhold a gated, unreleased result from
// any token that is not the approver: the web API as well as MCP, since agents
// hold tokens that authenticate on both ports.
//
// The markers are assembled at run time and never appear in a request's
// command, args or URL, so finding one in a response means output leaked, not
// that the request's own text was echoed back.
var (
	gatedStdoutMarker = "GATEDOUT" + "PUTQQ"
	gatedStderrMarker = "GATEDERR" + "PUTQQ"
	gatedHeaderMarker = "GATEDHDR" + "PUTQQ"
	gatedBodyMarker   = "GATEDBODY" + "PUTQQ"
)

var gatedMarkers = []string{gatedStdoutMarker, gatedStderrMarker, gatedHeaderMarker, gatedBodyMarker}

// leakedMarkers returns the gated-output markers present in s.
func leakedMarkers(s string) []string {
	var found []string
	for _, m := range gatedMarkers {
		if strings.Contains(s, m) {
			found = append(found, m)
		}
	}
	return found
}

// gatedFixture holds a relay with three finished, output-gated requests: a
// command that writes to stdout and stderr, the same command exiting non-zero
// (status "error"), and an http_request whose response carries a header and a
// body.
type gatedFixture struct {
	s          *TestServer
	c          *MCPClient
	cmdID      string
	errID      string // a gated command that exited non-zero, so its status is "error"
	httpID     string
	decider    string // the token that approved (approver, or the shared token in legacy mode)
	agentToken string // a per-client token minted for an agent
	events     *eventLog
}

// eventLog collects every /events frame an agent-token subscriber receives.
type eventLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (e *eventLog) String() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.buf.String()
}

func newGatedFixture(t *testing.T, approver bool) *gatedFixture {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Gated-Probe", gatedHeaderMarker)
		fmt.Fprint(w, gatedBodyMarker)
	}))
	t.Cleanup(upstream.Close)

	dataDir := t.TempDir()
	f := &gatedFixture{agentToken: mintClient(t, dataDir, "gated-reader"), decider: testToken}
	opts := []ServerOption{WithDataDir(dataDir)}
	if approver {
		opts = append(opts, WithApproverToken(approverToken))
		f.decider = approverToken
	}
	f.s = StartServer(t, opts...)
	f.c = NewMCPClient(t, f.s.MCPURL())
	initMCP(t, f.c)

	// Subscribe to /events as an agent before anything is decided, so every
	// frame about these requests is captured.
	f.events = &eventLog{}
	resp := WebGetResp(t, f.s.WebURL()+"/events", f.agentToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent GET /events: status %d", resp.StatusCode)
	}
	t.Cleanup(func() { resp.Body.Close() })
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			f.events.mu.Lock()
			f.events.buf.WriteString(sc.Text() + "\n")
			f.events.mu.Unlock()
		}
	}()

	f.cmdID = extractRequestID(t, f.c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "sh",
			"args":    []string{"-c", "printf '%s%s' GATEDOUT PUTQQ; printf '%s%s' GATEDERR PUTQQ >&2"},
			"reason":  "gated output read test",
		},
	}))
	f.errID = extractRequestID(t, f.c.Call(t, 4, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "sh",
			"args":    []string{"-c", "printf '%s%s' GATEDOUT PUTQQ; printf '%s%s' GATEDERR PUTQQ >&2; exit 3"},
			"reason":  "gated error-status read test",
		},
	}))
	f.httpID = extractRequestID(t, f.c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "http_request",
		"arguments": map[string]interface{}{
			"method": "GET",
			"url":    upstream.URL + "/probe",
			"reason": "gated http read test",
		},
	}))
	for _, id := range []string{f.cmdID, f.errID, f.httpID} {
		if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve-gated", f.s.WebURL(), id), f.decider, nil); code != http.StatusOK {
			t.Fatalf("approve-gated %s: status %d, body %s", id, code, body)
		}
		waitForStatus(t, f.s, id, "complete", "error")
		if _, gated := requestStatus(t, f.s, id); !gated {
			t.Fatalf("request %s is not output-gated after approve-gated", id)
		}
	}
	// The status-scoped reads below measure the error path only if this
	// request really ended there.
	if st, _ := requestStatus(t, f.s, f.errID); st != "error" {
		t.Fatalf("request %s (exits 3) has status %q, want \"error\"", f.errID, st)
	}
	// Let the final update frames reach the subscriber.
	time.Sleep(200 * time.Millisecond)
	return f
}

// webReads is every web-port read that returns request data.
func (f *gatedFixture) webReads() []string {
	return []string{
		"/api/requests",
		"/api/requests?status=complete",
		"/api/requests?status=error",
		"/api/requests/" + f.cmdID + "/content",
		"/api/requests/" + f.errID + "/content",
		"/api/requests/" + f.httpID + "/content",
	}
}

// mcpReads is every MCP tool call that returns a request's result.
func (f *gatedFixture) mcpReads() map[string]map[string]interface{} {
	return map[string]map[string]interface{}{
		"get_result/cmd":         {"name": "get_result", "arguments": map[string]interface{}{"request_id": f.cmdID}},
		"get_result/err":         {"name": "get_result", "arguments": map[string]interface{}{"request_id": f.errID}},
		"get_result/http":        {"name": "get_result", "arguments": map[string]interface{}{"request_id": f.httpID}},
		"get_result/cmd-wait":    {"name": "get_result", "arguments": map[string]interface{}{"request_id": f.cmdID, "timeout": float64(1)}},
		"get_result/http-wait":   {"name": "get_result", "arguments": map[string]interface{}{"request_id": f.httpID, "timeout": float64(1)}},
		"list_requests":          {"name": "list_requests", "arguments": map[string]interface{}{}},
		"list_requests/complete": {"name": "list_requests", "arguments": map[string]interface{}{"status": "complete"}},
		"list_requests/error":    {"name": "list_requests", "arguments": map[string]interface{}{"status": "error"}},
	}
}

func (f *gatedFixture) mcpText(t *testing.T, id int, params map[string]interface{}) string {
	t.Helper()
	raw, err := json.Marshal(f.c.Call(t, id, "tools/call", params))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestGatedOutput_AgentTokensCannotReadIt: with the approver configured, no
// agent token reads a gated, unreleased result on any path, and the approver
// reads all of it on the web port.
func TestGatedOutput_AgentTokensCannotReadIt(t *testing.T) {
	f := newGatedFixture(t, true)

	for _, tok := range []struct{ name, token string }{{"master", testToken}, {"per-client", f.agentToken}} {
		for _, path := range f.webReads() {
			code, body := WebGet(t, f.s.WebURL()+path, tok.token)
			if code != http.StatusOK {
				t.Fatalf("%s GET %s: status %d, want 200", tok.name, path, code)
			}
			if leaked := leakedMarkers(string(body)); len(leaked) > 0 {
				t.Errorf("%s token GET %s returned gated output %v before release", tok.name, path, leaked)
			}
		}
	}
	id := 100
	for name, params := range f.mcpReads() {
		id++
		if leaked := leakedMarkers(f.mcpText(t, id, params)); len(leaked) > 0 {
			t.Errorf("MCP %s returned gated output %v before release", name, leaked)
		}
	}
	if leaked := leakedMarkers(f.events.String()); len(leaked) > 0 {
		t.Errorf("agent /events stream carried gated output %v", leaked)
	}
	if !strings.Contains(f.events.String(), f.cmdID) {
		t.Fatal("control failed: the agent /events subscriber saw no frame for the gated request, so it measured nothing")
	}

	// The approver reads every part of the gated output on the web port.
	_, body := WebGet(t, f.s.WebURL()+"/api/requests", approverToken)
	if got := leakedMarkers(string(body)); len(got) != len(gatedMarkers) {
		t.Fatalf("approver GET /api/requests shows %v of the gated output, want all of %v", got, gatedMarkers)
	}
	// Positive control for the status=error read: the approver sees the
	// error-status request's output there, so an agent read of the same path
	// coming back clean means redaction, not an empty list.
	_, errBody := WebGet(t, f.s.WebURL()+"/api/requests?status=error", approverToken)
	if !strings.Contains(string(errBody), f.errID) || !strings.Contains(string(errBody), gatedStdoutMarker) || !strings.Contains(string(errBody), gatedStderrMarker) {
		t.Fatalf("control failed: approver GET /api/requests?status=error does not show request %s with its output, so the agent read of it measured nothing", f.errID)
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r["output_gated"] != true {
			t.Errorf("request %v: output_gated missing from the approver's view, so the dashboard cannot offer Release", r["id"])
		}
	}
}

// TestGatedOutput_ReleaseOpensItToAgents: after the approver releases, the
// same agent reads see the real output -- the positive control for the test
// above.
func TestGatedOutput_ReleaseOpensItToAgents(t *testing.T) {
	f := newGatedFixture(t, true)
	for _, id := range []string{f.cmdID, f.httpID} {
		if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/release", f.s.WebURL(), id), approverToken, nil); code != http.StatusOK {
			t.Fatalf("release %s: status %d, body %s", id, code, body)
		}
	}
	_, body := WebGet(t, f.s.WebURL()+"/api/requests", f.agentToken)
	if got := leakedMarkers(string(body)); len(got) != len(gatedMarkers) {
		t.Errorf("agent GET /api/requests after release shows %v, want all of %v", got, gatedMarkers)
	}
	cmd := f.mcpText(t, 201, f.mcpReads()["get_result/cmd"])
	httpText := f.mcpText(t, 202, f.mcpReads()["get_result/http"])
	if got := leakedMarkers(cmd + httpText); len(got) != len(gatedMarkers) {
		t.Errorf("agent MCP get_result after release shows %v, want all of %v", got, gatedMarkers)
	}
}

// TestGatedOutput_LegacyMode pins the behaviour with no approver configured:
// the web port cannot tell the operator's browser from an agent (they hold the
// same kind of token), so it returns gated output to every token as it always
// has; MCP keeps withholding it until release.
func TestGatedOutput_LegacyMode(t *testing.T) {
	f := newGatedFixture(t, false)
	_, body := WebGet(t, f.s.WebURL()+"/api/requests", f.agentToken)
	if got := leakedMarkers(string(body)); len(got) != len(gatedMarkers) {
		t.Errorf("legacy web GET /api/requests shows %v, want all of %v (legacy mode serves gated output to every web token)", got, gatedMarkers)
	}
	id := 300
	for name, params := range f.mcpReads() {
		id++
		if leaked := leakedMarkers(f.mcpText(t, id, params)); len(leaked) > 0 {
			t.Errorf("legacy MCP %s returned gated output %v before release", name, leaked)
		}
	}
}
