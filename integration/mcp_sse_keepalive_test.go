package integration

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"
)

// openSSE opens the MCP /sse stream with the master bearer. The MCP port is
// wrapped in AuthMiddleware, so the Authorization header is required.
func openSSE(t *testing.T, s *TestServer) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", s.MCPURL()+"/sse", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /sse: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("GET /sse: status %d", resp.StatusCode)
	}
	return resp
}

// readLinesUntil scans SSE lines until one equals want (returning everything
// seen, inclusive) or the timeout expires — and names what it was waiting for.
func readLinesUntil(t *testing.T, resp *http.Response, want string, timeout time.Duration) []string {
	t.Helper()
	lines := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var seen []string
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("SSE stream closed before %q arrived; saw %q", want, seen)
			}
			seen = append(seen, line)
			if line == want {
				return seen
			}
		case <-deadline:
			t.Fatalf("no %q within %s; saw %q", want, timeout, seen)
		}
	}
}

// The stream carries nothing between events, so a client cannot tell a dead
// connection from an idle one. A comment ping every keepalive interval is the
// traffic that makes both ends notice. Watched RED on the unmodified tree:
// no ": ping" within 3s.
func TestSSEKeepalivePingArrives(t *testing.T) {
	s := StartServer(t, WithKeepalive(1))
	resp := openSSE(t, s)
	defer resp.Body.Close()

	readLinesUntil(t, resp, ": ping", 3*time.Second)
}

// The keepalive must be additive: the endpoint frame still arrives first and
// unchanged, and the first ping comes after it.
func TestSSEKeepaliveDoesNotDisplaceEndpointFrame(t *testing.T) {
	s := StartServer(t, WithKeepalive(1))
	resp := openSSE(t, s)
	defer resp.Body.Close()

	seen := readLinesUntil(t, resp, ": ping", 3*time.Second)
	endpointIdx, pingIdx, dataIdx := -1, -1, -1
	for i, line := range seen {
		switch {
		case line == "event: endpoint" && endpointIdx == -1:
			endpointIdx = i
		case strings.HasPrefix(line, "data: /message?sessionId=") && dataIdx == -1:
			dataIdx = i
		case line == ": ping" && pingIdx == -1:
			pingIdx = i
		}
	}
	if endpointIdx != 0 {
		t.Fatalf("endpoint event must be the first line; saw %q", seen)
	}
	if dataIdx == -1 {
		t.Fatalf("endpoint frame's data line is missing or changed; saw %q", seen)
	}
	if pingIdx < dataIdx {
		t.Fatalf("keepalive ping must follow the endpoint frame; saw %q", seen)
	}
}
