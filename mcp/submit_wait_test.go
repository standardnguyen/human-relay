package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/store"
)

// approvingTools lists every tool that queues a request for human approval and
// answers with a request ID. It is written out by hand, not read from the
// implementation, so a tool that silently drops out of the wait set fails here
// instead of passing by construction.
var approvingTools = []string{
	"request_command_for_relay",
	"request_command_for_host",
	"exec_container",
	"exec_machine",
	"write_file",
	"http_request",
	"run_script",
	"create_script",
	"create_then_run",
	"install_relay_ssh",
	"install_ssh_key",
	"register_container",
	"delete_container",
	"register_machine",
	"delete_machine",
}

// nonApprovingTools answer immediately and never return a request to wait on.
// request_command is here because it is retired: it rejects every call.
var nonApprovingTools = []string{
	"request_command",
	"get_result",
	"list_requests",
	"list_containers",
	"list_machines",
	"withdraw_request",
}

func toolByName(name string) *Tool {
	for i := range ToolDefinitions {
		if ToolDefinitions[i].Name == name {
			return &ToolDefinitions[i]
		}
	}
	return nil
}

// Every tool must be classified, so a new approving tool cannot ship without
// someone deciding whether it takes `wait`.
func TestEveryToolIsClassifiedForWait(t *testing.T) {
	known := map[string]bool{}
	for _, n := range approvingTools {
		known[n] = true
	}
	for _, n := range nonApprovingTools {
		known[n] = true
	}
	for _, tool := range ToolDefinitions {
		if !known[tool.Name] {
			t.Errorf("tool %q is in neither approvingTools nor nonApprovingTools; decide whether it takes wait", tool.Name)
		}
	}
	for n := range known {
		if toolByName(n) == nil {
			t.Errorf("classified tool %q is not in ToolDefinitions", n)
		}
	}
}

func TestApprovingToolsAdvertiseWait(t *testing.T) {
	enforced := setup(t).maxWait
	for _, name := range approvingTools {
		tool := toolByName(name)
		if tool == nil {
			t.Errorf("%s: missing from ToolDefinitions", name)
			continue
		}
		p, ok := tool.InputSchema.Properties["wait"]
		if !ok {
			t.Errorf("%s: schema has no wait property", name)
			continue
		}
		if p.Type != "integer" {
			t.Errorf("%s: wait type = %q, want integer", name, p.Type)
		}
		// The model reading the schema has to learn the cap from it, and it
		// has to be the cap the handler enforces, not the compiled-in default.
		if want := fmt.Sprintf("at %d s", int(enforced/time.Second)); !strings.Contains(p.Description, want) {
			t.Errorf("%s: wait description does not state the enforced cap (%q): %q", name, want, p.Description)
		}
		for _, req := range tool.InputSchema.Required {
			if req == "wait" {
				t.Errorf("%s: wait must be optional", name)
			}
		}
	}
	for _, name := range nonApprovingTools {
		tool := toolByName(name)
		if tool == nil {
			continue
		}
		if _, ok := tool.InputSchema.Properties["wait"]; ok {
			t.Errorf("%s: a non-approving tool must not advertise wait", name)
		}
	}
}

// A bad wait is refused before anything is queued: an agent must never end up
// with a pending request it was told failed.
func TestSubmitWaitRejectsBadWaitBeforeQueuing(t *testing.T) {
	cases := []struct {
		name string
		wait interface{}
		want string
	}{
		{"string (stale client schema)", "30", "wait must be a number"},
		{"negative", float64(-1), "wait must be"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := setup(t)
			res := h.Handle("request_command_for_relay", map[string]interface{}{
				"command": "echo",
				"args":    []interface{}{"x"},
				"reason":  "bad wait",
				"wait":    c.wait,
			}, "")
			if !res.IsError {
				t.Fatalf("expected an error for wait=%v, got %s", c.wait, res.Content[0].Text)
			}
			if !strings.Contains(res.Content[0].Text, c.want) {
				t.Errorf("error %q does not contain %q", res.Content[0].Text, c.want)
			}
			if n := len(h.store.List("")); n != 0 {
				t.Errorf("a rejected call queued %d request(s); want 0", n)
			}
		})
	}
}

// A wait above the cap is clamped to the cap, however large. The clamp has to
// happen in seconds: multiplying first overflows time.Duration from
// 9223372037 s up, and the wrapped negative value passed both the clamp and
// the >= 0 check, so the call came back at once with no wait_expired.
func TestParseWaitClampsHugeValues(t *testing.T) {
	const max = 50 * time.Second
	cases := []struct {
		wait float64
		want time.Duration
	}{
		{49, 49 * time.Second},
		{50, max},
		{51, max},
		{9223372036, max}, // the largest value whose product still fits
		{9223372037, max}, // the smallest value whose product overflows
		{1e10, max},
		{1e12, max},
	}
	for _, c := range cases {
		got, errRes := parseWait(map[string]interface{}{"wait": c.wait}, max)
		if errRes != nil {
			t.Errorf("wait=%v: unexpected error %s", c.wait, errRes.Content[0].Text)
			continue
		}
		if got != c.want {
			t.Errorf("wait=%v: got %s, want %s", c.wait, got, c.want)
		}
	}
	// A cap under a second (tests set one) must not turn wait=0, "don't
	// wait", into a wait for the cap.
	if got, _ := parseWait(map[string]interface{}{"wait": float64(0)}, 300*time.Millisecond); got != 0 {
		t.Errorf("wait=0 under a 300ms cap: got %s, want 0", got)
	}
}

// The same overflow seen from the caller: a huge wait on an unapproved
// request has to hold for the cap and then report wait_expired, not return
// the plain pending response at once.
func TestSubmitWaitHugeWaitHoldsForTheCap(t *testing.T) {
	h := setup(t)
	h.maxWait = 300 * time.Millisecond
	start := time.Now()
	res := h.Handle("request_command_for_relay", map[string]interface{}{
		"command": "echo",
		"args":    []interface{}{"never-approved"},
		"reason":  "huge wait",
		"wait":    float64(1e10),
	}, "")
	elapsed := time.Since(start)
	m := decode(t, res)
	if m["wait_expired"] != true {
		t.Errorf("wait_expired = %v, want true (wait=1e10 must clamp to the cap, not wrap negative): %s", m["wait_expired"], res.Content[0].Text)
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("returned after %s; a clamped wait should hold for the %s cap", elapsed, h.maxWait)
	}
}

// settleWhenQueued waits for the first request to appear, then approves it and
// records a result the way the web handler's executor would.
func settleWhenQueued(t *testing.T, s *store.Store, result *store.Result, status store.Status) <-chan string {
	ids := make(chan string, 1)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if list := s.List(""); len(list) > 0 {
				id := list[0].ID
				time.Sleep(300 * time.Millisecond) // let the caller start waiting
				s.Approve(id, false)
				s.SetResult(id, result, status)
				ids <- id
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		ids <- ""
	}()
	return ids
}

func decode(t *testing.T, res *CallToolResult) map[string]interface{} {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content[0].Text)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &m); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, res.Content[0].Text)
	}
	return m
}

// The settled payload is get_result's, and the submission's advisory fields
// (here a shell-metacharacter warning) survive rather than being swallowed by
// the wait.
func TestSubmitWaitReturnsGetResultPayloadAndKeepsWarnings(t *testing.T) {
	h := setup(t)
	ids := settleWhenQueued(t, h.store, &store.Result{ExitCode: 0, Stdout: "a|b\n"}, store.StatusComplete)

	start := time.Now()
	res := h.Handle("request_command_for_relay", map[string]interface{}{
		"command": "echo",
		"args":    []interface{}{"a|b"},
		"reason":  "wait unit test",
		"wait":    float64(5),
	}, "")
	elapsed := time.Since(start)
	id := <-ids
	if id == "" {
		t.Fatal("request was never queued")
	}

	m := decode(t, res)
	if m["status"] != "complete" {
		t.Fatalf("status = %v, want complete (the wait should have returned the settled request): %s", m["status"], res.Content[0].Text)
	}
	if m["id"] != id {
		t.Errorf("id = %v, want %s", m["id"], id)
	}
	result, _ := m["result"].(map[string]interface{})
	if result == nil || result["stdout"] != "a|b\n" {
		t.Errorf("result = %v, want stdout a|b", m["result"])
	}
	sub, _ := m["submission"].(map[string]interface{})
	if sub == nil || sub["warnings"] == nil {
		t.Errorf("submission warnings were lost: %s", res.Content[0].Text)
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("returned after %s, before the request was decided", elapsed)
	}
}

// openSSE connects to the in-process MCP server, returns the message endpoint
// and a func that drops the stream. A drain goroutine keeps reading so the
// server never blocks on a full socket; it exits when the stream closes.
func openSSE(t *testing.T, hc *http.Client, base string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/sse", nil)
	resp, err := hc.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("sse connect: %v", err)
	}
	sc := bufio.NewScanner(resp.Body)
	endpoint := ""
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			endpoint = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if endpoint == "" {
		cancel()
		resp.Body.Close()
		t.Fatal("no endpoint event")
	}
	go func() {
		for sc.Scan() {
		}
	}()
	return endpoint, func() {
		cancel()
		resp.Body.Close()
	}
}

func waitCallBody(wait int) []byte {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "request_command_for_relay",
			"arguments": map[string]interface{}{
				"command": "echo",
				"args":    []string{"never-approved"},
				"reason":  "disconnect test",
				"wait":    wait,
			},
		},
	})
	return body
}

func eventually(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func goroutineDump() string {
	buf := make([]byte, 1<<20)
	return string(buf[:runtime.Stack(buf, true)])
}

// A held call whose client goes away must stop waiting and leave no goroutine
// behind, and the request itself must stay pending and approvable. Two ways a
// client goes away are covered: the SSE stream (the MCP session) closes, or
// the POST carrying the call is dropped while the stream stays up.
func TestSubmitWaitStopsWhenClientDisconnects(t *testing.T) {
	cases := []struct {
		name string
		// drop severs the client in one of the two ways, given the SSE closer
		// and the POST's cancel func.
		drop func(closeSSE, cancelPost func())
		// sseOpenAfter says whether the stream is still up once dropped, which
		// decides what the goroutine count has to return to.
		sseOpenAfter bool
	}{
		{"sse stream closes", func(closeSSE, _ func()) { closeSSE() }, false},
		{"post dropped, stream stays", func(_, cancelPost func()) { cancelPost() }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := setup(t)
			srv := httptest.NewServer(NewServer(h))
			defer srv.Close()
			hc := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

			base := runtime.NumGoroutine()
			endpoint, closeSSE := openSSE(t, hc, srv.URL)
			defer closeSSE()
			time.Sleep(100 * time.Millisecond)
			baseWithSSE := runtime.NumGoroutine()

			postCtx, cancelPost := context.WithCancel(context.Background())
			defer cancelPost()
			postDone := make(chan struct{})
			go func() {
				defer close(postDone)
				req, _ := http.NewRequestWithContext(postCtx, http.MethodPost, srv.URL+endpoint, bytes.NewReader(waitCallBody(30)))
				req.Header.Set("Content-Type", "application/json")
				if resp, err := hc.Do(req); err == nil {
					resp.Body.Close()
				}
			}()

			if !eventually(2*time.Second, func() bool { return len(h.store.List("")) == 1 }) {
				t.Fatal("the call never queued its request")
			}
			select {
			case <-postDone:
				t.Fatal("the call returned while its request was still pending: wait was not honoured")
			case <-time.After(700 * time.Millisecond):
			}

			c.drop(closeSSE, cancelPost)
			select {
			case <-postDone:
			case <-time.After(3 * time.Second):
				t.Fatal("client side of the POST never finished")
			}

			want := base
			if c.sseOpenAfter {
				want = baseWithSSE
			}
			if !eventually(3*time.Second, func() bool { return runtime.NumGoroutine() <= want }) {
				t.Fatalf("goroutines did not return to %d after the client left (now %d): the held call is still waiting\n%s",
					want, runtime.NumGoroutine(), goroutineDump())
			}

			r := h.store.List("")[0]
			if r.Status != store.StatusPending {
				t.Fatalf("request status = %s after the client left; want pending", r.Status)
			}
			if ok, _ := h.store.Approve(r.ID, false); !ok {
				t.Error("request could not be approved after the waiting client left")
			}
		})
	}
}

// MHR_MAX_WAIT can only lower the cap. The cap exists to keep a held call
// under the client's own timeout, so raising it past maxSubmitWait, or a value
// that cannot be read, falls back to the default rather than lifting it.
func TestMaxWaitFromEnvOnlyLowers(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"", maxSubmitWait},
		{"2", 2 * time.Second},
		{"49", 49 * time.Second},
		{"50", maxSubmitWait},
		{"500", maxSubmitWait}, // above the cap: not a way to raise it
		{"0", maxSubmitWait},
		{"-5", maxSubmitWait},
		{"abc", maxSubmitWait},
		{"1.5", maxSubmitWait},
	}
	for _, c := range cases {
		t.Setenv("MHR_MAX_WAIT", c.value)
		if got := maxWaitFromEnv(); got != c.want {
			t.Errorf("MHR_MAX_WAIT=%q: got %s, want %s", c.value, got, c.want)
		}
	}
	if maxSubmitWait >= 60*time.Second {
		t.Errorf("maxSubmitWait = %s; it must stay below the MCP TypeScript SDK's 60s default request timeout", maxSubmitWait)
	}
}
