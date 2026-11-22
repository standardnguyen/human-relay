// Package testrelay boots a real relay binary for the TUI's integration tests
// and files requests against it through the MCP port with the agent token.
//
// It reuses the relay's own harness (StartServer and friends) and adds the few
// helpers that live only in that harness's _test files upstream.
package testrelay

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/standardnguyen/human-relay/integration"
)

const (
	// AgentToken is the master token the harness always configures.
	AgentToken = "test-token-for-ci"
	// ApproverToken is a test-only value, never a real credential.
	ApproverToken = "mhra_test_tui_approver_0123456789abcdef"
)

// Relay is a running relay plus an initialised MCP session.
type Relay struct {
	Server *integration.TestServer
	MCP    *integration.MCPClient
	nextID int
}

// Bin reports the relay binary named by HUMAN_RELAY_BIN, or "".
func Bin() string { return os.Getenv("HUMAN_RELAY_BIN") }

// Start boots a relay. Without HUMAN_RELAY_BIN the test is skipped, because
// there is nothing to boot; CI and `make test-tui` always set it.
func Start(t *testing.T, opts ...integration.ServerOption) *Relay {
	t.Helper()
	if Bin() == "" {
		t.Skip("HUMAN_RELAY_BIN not set; build the root binary and point it at it to run relay-backed tests")
	}
	s := integration.StartServer(t, opts...)
	c := integration.NewMCPClient(t, s.MCPURL())
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "tui-test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)
	return &Relay{Server: s, MCP: c, nextID: 10}
}

// WithApprover starts a relay that only ApproverToken may decide on.
func WithApprover(t *testing.T, opts ...integration.ServerOption) *Relay {
	t.Helper()
	return Start(t, append([]integration.ServerOption{integration.WithApproverToken(ApproverToken)}, opts...)...)
}

// URL is the web (dashboard/API) base URL.
func (r *Relay) URL() string { return r.Server.WebURL() }

// Call invokes an MCP tool and returns the first text content.
func (r *Relay) Call(t *testing.T, tool string, args map[string]interface{}) string {
	t.Helper()
	r.nextID++
	resp := r.MCP.Call(t, r.nextID, "tools/call", map[string]interface{}{"name": tool, "arguments": args})
	if resp.Error != nil {
		t.Fatalf("%s: rpc error %d %s", tool, resp.Error.Code, resp.Error.Message)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil || len(out.Content) == 0 {
		t.Fatalf("%s: no content in %s", tool, resp.Result)
	}
	return out.Content[0].Text
}

// Submit files a request through tool and returns its request_id.
func (r *Relay) Submit(t *testing.T, tool string, args map[string]interface{}) string {
	t.Helper()
	text := r.Call(t, tool, args)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("%s: response is not JSON: %q", tool, text)
	}
	id, _ := parsed["request_id"].(string)
	if id == "" {
		t.Fatalf("%s: no request_id in %q", tool, text)
	}
	return id
}

// Command files a plain argv request for the relay container.
func (r *Relay) Command(t *testing.T, command string, args []string, reason string) string {
	t.Helper()
	return r.Submit(t, "request_command_for_relay", map[string]interface{}{
		"command": command, "args": args, "reason": reason,
	})
}

// Shell files a shell-mode request.
func (r *Relay) Shell(t *testing.T, command, reason string) string {
	t.Helper()
	return r.Submit(t, "request_command_for_relay", map[string]interface{}{
		"command": command, "shell": true, "reason": reason,
	})
}

// HTTP files an http_request.
func (r *Relay) HTTP(t *testing.T, method, url string, headers map[string]string, body, reason string) string {
	t.Helper()
	args := map[string]interface{}{"method": method, "url": url, "reason": reason}
	if headers != nil {
		args["headers"] = headers
	}
	if body != "" {
		args["body"] = body
	}
	return r.Submit(t, "http_request", args)
}

// WriteFile files a write_file request (stdin carries the content).
func (r *Relay) WriteFile(t *testing.T, path, content, reason string) string {
	t.Helper()
	return r.Submit(t, "write_file", map[string]interface{}{
		"path": path, "content": content, "reason": reason, "host": "127.0.0.1",
	})
}
