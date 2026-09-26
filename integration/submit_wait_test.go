package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Submit-and-wait: every approving tool takes an optional `wait` (seconds).
// With wait > 0 the call holds until the request is decided and has run, then
// returns get_result's payload; if the wait runs out first it returns the
// usual pending response. wait never approves anything: it only changes when
// the response is sent.

// withMaxWait lowers the server-side cap on wait (MHR_MAX_WAIT) so a clamp can
// be observed without sitting through the production cap.
func withMaxWait(seconds int) ServerOption {
	return func(s *TestServer) {
		s.env = append(s.env, fmt.Sprintf("MHR_MAX_WAIT=%d", seconds))
	}
}

// waitJSON decodes the first text block of a tools/call response.
func waitJSON(t *testing.T, resp *JSONRPCResponse) map[string]interface{} {
	t.Helper()
	if isErrorResponse(resp) {
		t.Fatalf("tool returned an error: %s", toolText(t, resp))
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(toolText(t, resp)), &m); err != nil {
		t.Fatalf("tool text is not JSON: %v\n%s", err, toolText(t, resp))
	}
	return m
}

func callTool(t *testing.T, c *MCPClient, id int, name string, args map[string]interface{}) (*JSONRPCResponse, time.Duration) {
	t.Helper()
	start := time.Now()
	resp := c.Call(t, id, "tools/call", map[string]interface{}{"name": name, "arguments": args})
	return resp, time.Since(start)
}

func stdoutOf(m map[string]interface{}) string {
	r, _ := m["result"].(map[string]interface{})
	if r == nil {
		return ""
	}
	s, _ := r["stdout"].(string)
	return s
}

// decideFirstPending runs in a goroutine while a wait call is held: it finds
// the single pending request through the dashboard API and posts action
// (approve / approve-gated / deny) to it after delay. It reports through the
// returned channel instead of t, which must not be used off the test goroutine.
func decideFirstPending(s *TestServer, action string, delay time.Duration, payload interface{}) <-chan error {
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		var id string
		for time.Now().Before(deadline) && id == "" {
			req, _ := http.NewRequest(http.MethodGet, s.WebURL()+"/api/requests?status=pending", nil)
			req.Header.Set("Authorization", "Bearer "+s.token)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				var list []struct {
					ID string `json:"id"`
				}
				json.NewDecoder(resp.Body).Decode(&list)
				resp.Body.Close()
				if len(list) > 0 {
					id = list[0].ID
				}
			}
			if id == "" {
				time.Sleep(50 * time.Millisecond)
			}
		}
		if id == "" {
			done <- fmt.Errorf("no pending request appeared")
			return
		}
		time.Sleep(delay)
		var body io.Reader
		if payload != nil {
			b, _ := json.Marshal(payload)
			body = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/requests/%s/%s", s.WebURL(), id, action), body)
		req.Header.Set("Authorization", "Bearer "+s.token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- err
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			done <- fmt.Errorf("%s returned %d", action, resp.StatusCode)
			return
		}
		done <- nil
	}()
	return done
}

func TestSubmitWaitZeroIsUnchanged(t *testing.T) {
	_, c := initClient(t)
	for i, args := range []map[string]interface{}{
		{"command": "echo", "args": []string{"no-wait"}, "reason": "no wait arg"},
		{"command": "echo", "args": []string{"zero-wait"}, "reason": "wait 0", "wait": 0},
	} {
		resp, elapsed := callTool(t, c, 10+i, "request_command_for_relay", args)
		m := waitJSON(t, resp)
		if m["status"] != "pending" || m["request_id"] == "" || m["request_id"] == nil {
			t.Errorf("case %d: got %v, want pending + request_id", i, m)
		}
		if len(m) != 2 {
			t.Errorf("case %d: response has keys beyond request_id/status: %v", i, m)
		}
		if elapsed > 1500*time.Millisecond {
			t.Errorf("case %d: took %s; a call without wait must return at once", i, elapsed)
		}
	}
}

// The card's motivating case: an auto-approved script finishes at once, so one
// call should carry its output back. create_then_run and run_script go
// through the same script executor and the same wait.
func TestSubmitWaitWhitelistedScriptReturnsInline(t *testing.T) {
	const body = "#!/bin/sh\necho \"hello-$1\"\n"
	sum := sha256.Sum256([]byte(body))
	wlPath := filepath.Join(t.TempDir(), "whitelist.json")
	rules, _ := json.Marshal([]map[string]interface{}{
		{"command": "create_then_run", "args": []string{"oneshot/wait-hello", hex.EncodeToString(sum[:])}},
	})
	os.WriteFile(wlPath, rules, 0644)

	_, c := initClient(t, WithWhitelistFile(wlPath), WithScriptsDir(t.TempDir()))
	resp, elapsed := callTool(t, c, 10, "create_then_run", map[string]interface{}{
		"name":    "wait-hello",
		"content": body,
		"args":    []string{"world"},
		"reason":  "whitelisted script with wait",
		"wait":    10,
	})
	m := waitJSON(t, resp)
	if m["status"] != "complete" {
		t.Fatalf("status = %v, want complete inline: %v", m["status"], m)
	}
	if got := stdoutOf(m); got != "hello-world\n" {
		t.Errorf("stdout = %q, want hello-world", got)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %s for an auto-approved script", elapsed)
	}
}

// http_request goes through the same wait, against a local endpoint.
func TestSubmitWaitWhitelistedHTTPReturnsInline(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "pong")
	}))
	defer target.Close()
	url := target.URL + "/ping"

	wlPath := filepath.Join(t.TempDir(), "whitelist.json")
	rules, _ := json.Marshal([]map[string]interface{}{{"command": "GET", "args": []string{url}}})
	os.WriteFile(wlPath, rules, 0644)

	_, c := initClient(t, WithWhitelistFile(wlPath))
	resp, _ := callTool(t, c, 10, "http_request", map[string]interface{}{
		"method": "GET",
		"url":    url,
		"reason": "whitelisted GET with wait",
		"wait":   10,
	})
	m := waitJSON(t, resp)
	if m["status"] != "complete" {
		t.Fatalf("status = %v, want complete inline: %v", m["status"], m)
	}
	if got := stdoutOf(m); got != "pong" {
		t.Errorf("stdout = %q, want pong", got)
	}
}

func TestSubmitWaitReturnsWhenApprovedDuringWait(t *testing.T) {
	s, c := initClient(t)
	decided := decideFirstPending(s, "approve", time.Second, nil)
	resp, elapsed := callTool(t, c, 10, "request_command_for_relay", map[string]interface{}{
		"command": "echo",
		"args":    []string{"approved-mid-wait"},
		"reason":  "approve during wait",
		"wait":    15,
	})
	if err := <-decided; err != nil {
		t.Fatalf("approving: %v", err)
	}
	m := waitJSON(t, resp)
	if m["status"] != "complete" {
		t.Fatalf("status = %v, want complete: %v", m["status"], m)
	}
	if got := stdoutOf(m); got != "approved-mid-wait\n" {
		t.Errorf("stdout = %q", got)
	}
	if elapsed < time.Second {
		t.Errorf("returned after %s, before the approval was made", elapsed)
	}
	if elapsed > 8*time.Second {
		t.Errorf("returned after %s; should follow the approval promptly", elapsed)
	}
}

// Approval is not the end of the wait: the call holds until the approved
// command has finished running, so the caller gets its result, not "running".
func TestSubmitWaitHoldsUntilTheRunFinishes(t *testing.T) {
	s, c := initClient(t)
	decided := decideFirstPending(s, "approve", 300*time.Millisecond, nil)
	resp, elapsed := callTool(t, c, 10, "request_command_for_relay", map[string]interface{}{
		"command": "sleep",
		"args":    []string{"2"},
		"reason":  "slow command",
		"wait":    15,
	})
	if err := <-decided; err != nil {
		t.Fatalf("approving: %v", err)
	}
	m := waitJSON(t, resp)
	if m["status"] != "complete" {
		t.Fatalf("status = %v, want complete (returned before the run finished): %v", m["status"], m)
	}
	if elapsed < 2*time.Second {
		t.Errorf("returned after %s, before a 2s command could finish", elapsed)
	}
}

func TestSubmitWaitExpiryReturnsPendingAndGetResultStillWorks(t *testing.T) {
	s, c := initClient(t)
	resp, elapsed := callTool(t, c, 10, "request_command_for_relay", map[string]interface{}{
		"command": "echo",
		"args":    []string{"late"},
		"reason":  "expiry",
		"wait":    2,
	})
	m := waitJSON(t, resp)
	if m["status"] != "pending" {
		t.Fatalf("status = %v, want pending on expiry: %v", m["status"], m)
	}
	id, _ := m["request_id"].(string)
	if id == "" {
		t.Fatalf("no request_id on expiry: %v", m)
	}
	if m["wait_expired"] != true {
		t.Errorf("wait_expired = %v, want true", m["wait_expired"])
	}
	if m["current_status"] != "pending" {
		t.Errorf("current_status = %v, want pending", m["current_status"])
	}
	if elapsed < 2*time.Second || elapsed > 5*time.Second {
		t.Errorf("returned after %s, want about 2s", elapsed)
	}

	// The request is untouched: still pending, still approvable, still pollable.
	resp = c.Call(t, 11, "tools/call", map[string]interface{}{
		"name": "get_result", "arguments": map[string]interface{}{"request_id": id},
	})
	if r := extractResult(t, resp); r.Status != "pending" {
		t.Fatalf("get_result after expiry: status %s, want pending", r.Status)
	}
	if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id), s.token, nil); code != 200 {
		t.Fatalf("approve after expiry: %d %s", code, body)
	}
	r := pollUntilDone(t, c, 12, id)
	if r.Status != "complete" || r.Result == nil || r.Result.Stdout != "late\n" {
		t.Fatalf("after approve: status %s result %+v", r.Status, r.Result)
	}
}

func TestSubmitWaitDenialReturnsPromptly(t *testing.T) {
	s, c := initClient(t)
	decided := decideFirstPending(s, "deny", time.Second, map[string]string{"reason": "not today"})
	resp, elapsed := callTool(t, c, 10, "request_command_for_relay", map[string]interface{}{
		"command": "echo",
		"args":    []string{"denied"},
		"reason":  "deny during wait",
		"wait":    15,
	})
	if err := <-decided; err != nil {
		t.Fatalf("denying: %v", err)
	}
	m := waitJSON(t, resp)
	if m["status"] != "denied" {
		t.Fatalf("status = %v, want denied: %v", m["status"], m)
	}
	if m["deny_reason"] != "not today" {
		t.Errorf("deny_reason = %v", m["deny_reason"])
	}
	if m["result"] != nil {
		t.Errorf("a denied request has a result: %v", m["result"])
	}
	if elapsed > 5*time.Second {
		t.Errorf("denial took %s to come back", elapsed)
	}
}

// Gating is untouched by the wait: the content stays withheld and the caller
// is told it is gated.
func TestSubmitWaitGatedOutputStaysGated(t *testing.T) {
	t.Run("whitelist gate_output", func(t *testing.T) {
		wlPath := filepath.Join(t.TempDir(), "whitelist.json")
		rules, _ := json.Marshal([]map[string]interface{}{
			{"command": "echo", "args": []string{"secret-wl"}, "gate_output": true},
		})
		os.WriteFile(wlPath, rules, 0644)
		_, c := initClient(t, WithWhitelistFile(wlPath))
		resp, _ := callTool(t, c, 10, "request_command_for_relay", map[string]interface{}{
			"command": "echo", "args": []string{"secret-wl"}, "reason": "gated whitelist", "wait": 10,
		})
		assertGated(t, waitJSON(t, resp), "secret-wl")
	})
	t.Run("approve-gated during wait", func(t *testing.T) {
		s, c := initClient(t)
		decided := decideFirstPending(s, "approve-gated", 500*time.Millisecond, nil)
		resp, _ := callTool(t, c, 10, "request_command_for_relay", map[string]interface{}{
			"command": "echo", "args": []string{"secret-manual"}, "reason": "gated approve", "wait": 10,
		})
		if err := <-decided; err != nil {
			t.Fatalf("approve-gated: %v", err)
		}
		assertGated(t, waitJSON(t, resp), "secret-manual")
	})
}

func assertGated(t *testing.T, m map[string]interface{}, secret string) {
	t.Helper()
	if m["status"] != "complete" {
		t.Fatalf("status = %v, want complete: %v", m["status"], m)
	}
	if m["output_gated"] != true {
		t.Errorf("output_gated = %v, want true", m["output_gated"])
	}
	// The secret is the echo argument, so it legitimately appears in args; it
	// must not appear as output.
	if out := stdoutOf(m); strings.Contains(out, secret) || !strings.Contains(out, "output gated by operator") {
		t.Errorf("stdout = %q, want the gate placeholder and no content", out)
	}
}

// The cap is enforced server-side and a larger wait is clamped, not refused.
func TestSubmitWaitClampedToServerCap(t *testing.T) {
	_, c := initClient(t, withMaxWait(2))
	resp, elapsed := callTool(t, c, 10, "request_command_for_relay", map[string]interface{}{
		"command": "echo", "args": []string{"clamped"}, "reason": "clamp", "wait": 25,
	})
	m := waitJSON(t, resp)
	if m["status"] != "pending" || m["wait_expired"] != true {
		t.Fatalf("got %v, want an expired pending response", m)
	}
	if ws, _ := m["wait_seconds"].(float64); ws != 2 {
		t.Errorf("wait_seconds = %v, want the clamped 2", m["wait_seconds"])
	}
	if elapsed > 5*time.Second {
		t.Errorf("held %s; the 2s cap was not applied", elapsed)
	}
}

// The schema states the cap in force, not the compiled-in default. A model
// plans its waits from the description, so with MHR_MAX_WAIT=2 every approving
// tool has to say 2 s, not 50 s.
func TestSubmitWaitSchemaStatesTheEffectiveCap(t *testing.T) {
	_, c := initClient(t, withMaxWait(2))
	resp := c.Call(t, 2, "tools/list", nil)
	if resp.Error != nil {
		t.Fatalf("tools/list returned error: %s", resp.Error.Message)
	}
	var result struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("tools/list result: %v", err)
	}
	advertised := 0
	for _, tool := range result.Tools {
		p, ok := tool.InputSchema.Properties["wait"]
		if !ok {
			continue
		}
		advertised++
		if !strings.Contains(p.Description, "at 2 s") || strings.Contains(p.Description, "50") {
			t.Errorf("%s: wait description does not state the 2 s cap in force: %q", tool.Name, p.Description)
		}
	}
	if advertised == 0 {
		t.Fatal("no tool advertises wait; the check above ran on nothing")
	}
}

// A client that goes away mid-wait leaves the request intact: it is still
// pending, still approvable, and still retrievable by a new session.
func TestSubmitWaitClientDisconnectLeavesRequestIntact(t *testing.T) {
	s := StartServer(t)

	// A bare session, so the test controls exactly when each half closes.
	sseCtx, closeSSE := context.WithCancel(context.Background())
	defer closeSSE()
	sseReq, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, s.MCPURL()+"/sse", nil)
	sseReq.Header.Set("Authorization", "Bearer "+s.token)
	sseResp, err := http.DefaultClient.Do(sseReq)
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	defer sseResp.Body.Close()
	sc := bufio.NewScanner(sseResp.Body)
	endpoint := ""
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			endpoint = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if endpoint == "" {
		t.Fatal("no endpoint event")
	}
	go func() {
		for sc.Scan() {
		}
	}()

	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]interface{}{
			"name": "request_command_for_relay",
			"arguments": map[string]interface{}{
				"command": "echo", "args": []string{"survives"}, "reason": "disconnect", "wait": 30,
			},
		},
	})
	postCtx, dropPost := context.WithCancel(context.Background())
	defer dropPost()
	postDone := make(chan struct{})
	go func() {
		defer close(postDone)
		req, _ := http.NewRequestWithContext(postCtx, http.MethodPost, s.MCPURL()+endpoint, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+s.token)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	// Find the queued request through the dashboard, then check the call is
	// genuinely being held before cutting the client off.
	var id string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && id == "" {
		_, b := WebGet(t, s.WebURL()+"/api/requests?status=pending", s.token)
		var list []struct {
			ID string `json:"id"`
		}
		json.Unmarshal(b, &list)
		if len(list) > 0 {
			id = list[0].ID
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if id == "" {
		t.Fatal("request never queued")
	}
	select {
	case <-postDone:
		t.Fatal("the call returned while its request was pending: wait was not honoured")
	case <-time.After(time.Second):
	}

	dropPost()
	closeSSE()
	<-postDone

	c := NewMCPClient(t, s.MCPURL())
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "get_result", "arguments": map[string]interface{}{"request_id": id},
	})
	if r := extractResult(t, resp); r.Status != "pending" {
		t.Fatalf("after disconnect: status %s, want pending", r.Status)
	}
	if code, b := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id), s.token, nil); code != 200 {
		t.Fatalf("approve after disconnect: %d %s", code, b)
	}
	r := pollUntilDone(t, c, 3, id)
	if r.Status != "complete" || r.Result == nil || r.Result.Stdout != "survives\n" {
		t.Fatalf("after approve: status %s result %+v", r.Status, r.Result)
	}
}
