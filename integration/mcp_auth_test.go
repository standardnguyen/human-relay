package integration

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The MCP/SSE port carries the full tool surface (request_command, write_file,
// exec_container, ...). It must require the same bearer token as the web port.
// Finding #23 (part 1): it was previously served unauthenticated.

func TestMCPSSERejectsMissingToken(t *testing.T) {
	s := StartServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.MCPURL()+"/sse", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /sse: got status %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestMCPSSERejectsWrongToken(t *testing.T) {
	s := StartServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.MCPURL()+"/sse", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer not-the-real-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token /sse: got status %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestMCPSSEAcceptsValidToken(t *testing.T) {
	s := StartServer(t)

	// Cancelling the context is what closes the (otherwise endless) SSE stream.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.MCPURL()+"/sse", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated /sse: got status %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// The stream must actually work through the middleware, not just return 200:
	// the server sends an endpoint event naming the message URL for this session.
	var endpoint string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
			endpoint = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if !strings.HasPrefix(endpoint, "/message?sessionId=") {
		t.Fatalf("endpoint event: got %q, want a /message?sessionId= URL", endpoint)
	}
}

func TestMCPMessageRejectsMissingToken(t *testing.T) {
	s := StartServer(t)

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req, err := http.NewRequest(http.MethodPost, s.MCPURL()+"/message?sessionId=session-1", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("message POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /message: got status %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// The authenticated MCP path still works end to end: a real client can list tools.
func TestMCPClientWorksWithAuth(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "auth-test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/list", nil)
	if resp.Error != nil {
		t.Fatalf("tools/list returned error: %+v", resp.Error)
	}
	if !strings.Contains(string(resp.Result), "request_command") {
		t.Fatalf("tools/list result missing request_command: %s", resp.Result)
	}
}
