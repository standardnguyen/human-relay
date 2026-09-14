package integration

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// warmClient returns an HTTP client whose connection to the relay's web port is
// already established, so a later request starts writing immediately instead of
// spending a TCP handshake outside the code under test.
func warmClient(t *testing.T, base, token string) *http.Client {
	t.Helper()
	c := &http.Client{
		Transport: &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 4},
		Timeout:   10 * time.Second,
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/requests", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("warmup request failed: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return c
}

// postApprove sends one approve POST on the given client and returns its status
// code, draining the body so the connection stays warm for the next round.
func postApprove(t *testing.T, c *http.Client, url, token string) int {
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Errorf("build approve request: %v", err)
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		t.Errorf("approve POST failed: %v", err)
		return 0
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// TestConcurrentApproveExecutesOnce covers finding #52: the approve path used
// to read the request, check it was pending, and only later flip it to
// approved, with no lock held across the three steps. Two near-simultaneous
// approvals of the SAME pending request could both pass the pending check and
// both spawn an execution, so a single human approval ran the command twice.
//
// Exactly one of the two calls must succeed, and the request must execute
// exactly once.
func TestConcurrentApproveExecutesOnce(t *testing.T) {
	dataDir := t.TempDir()
	s := StartServer(t, WithDataDir(dataDir))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// One pair of concurrent approvals hits the old window only rarely, so the
	// test repeats it: every round must still land exactly one approval and
	// exactly one execution.
	const rounds = 60
	const callers = 6

	ids := make([]string, rounds)
	for i := 0; i < rounds; i++ {
		resp := c.Call(t, 2+i, "tools/call", map[string]interface{}{
			"name": "request_command_for_relay",
			"arguments": map[string]interface{}{
				"command": "echo",
				"args":    []string{fmt.Sprintf("approve-race-%d", i)},
				"reason":  "double-approve race test",
			},
		})
		ids[i] = extractRequestID(t, resp)
	}

	// Each caller gets its own already-warm keep-alive connection: dialing
	// inside the race window would spread the arrivals far wider than the
	// window itself and the test would never exercise the bug.
	clients := make([]*http.Client, callers)
	for j := range clients {
		clients[j] = warmClient(t, s.WebURL(), s.token)
	}

	for i, requestID := range ids {
		// Fire the approvals from a common starting gun so they overlap.
		codes := make([]int, callers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := 0; j < callers; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				<-start
				codes[j] = postApprove(t, clients[j],
					fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID), s.token)
			}(j)
		}
		close(start)
		wg.Wait()

		accepted, rejected := 0, 0
		for _, code := range codes {
			switch code {
			case 200:
				accepted++
			case 409:
				rejected++
			default:
				t.Errorf("round %d: unexpected approve status %d (want 200 or 409)", i, code)
			}
		}
		if accepted != 1 {
			t.Errorf("round %d: expected exactly 1 successful approve, got %d (codes: %v)", i, accepted, codes)
		}
		if rejected != callers-1 {
			t.Errorf("round %d: expected exactly %d rejected approves, got %d (codes: %v)", i, callers-1, rejected, codes)
		}
	}

	// Let the executions finish and land in the audit log.
	time.Sleep(time.Second)

	entries := readAuditLog(t, filepath.Join(dataDir, "audit.log"))
	approvals := make(map[string]int)
	starts := make(map[string]int)
	for _, e := range entries {
		switch e.Event {
		case "request_approved":
			approvals[e.RequestID]++
		case "execution_started":
			starts[e.RequestID]++
		}
	}
	for i, requestID := range ids {
		if approvals[requestID] != 1 {
			t.Errorf("round %d (%s): expected exactly 1 request_approved audit entry, got %d",
				i, requestID, approvals[requestID])
		}
		if starts[requestID] != 1 {
			t.Errorf("round %d (%s): expected exactly 1 execution_started audit entry (double execution!), got %d",
				i, requestID, starts[requestID])
		}
	}
}

// TestApproveAfterDenyConflicts pins the non-concurrent half of the same guard:
// a request that has already been decided cannot be approved afterwards.
func TestApproveAfterDenyConflicts(t *testing.T) {
	dataDir := t.TempDir()
	s := StartServer(t, WithDataDir(dataDir))
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
			"args":    []string{"already-denied"},
			"reason":  "approve-after-deny test",
		},
	})
	requestID := extractRequestID(t, resp)

	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), requestID),
		s.token, map[string]string{"reason": "nope"})
	if code != 200 {
		t.Fatalf("deny returned status %d", code)
	}

	code, body := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 409 {
		t.Fatalf("approve after deny: expected 409, got %d (body: %s)", code, body)
	}

	time.Sleep(300 * time.Millisecond)
	entries := readAuditLog(t, filepath.Join(dataDir, "audit.log"))
	for _, e := range entries {
		if e.RequestID == requestID && e.Event == "execution_started" {
			t.Fatal("denied request must never execute")
		}
	}
}
