package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMCPInitialize(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	resp := c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "test-client",
			"version": "1.0.0",
		},
	})

	if resp.Error != nil {
		t.Fatalf("initialize returned error: %s", resp.Error.Message)
	}

	var result map[string]interface{}
	json.Unmarshal(resp.Result, &result)
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("expected protocol version 2024-11-05, got %v", result["protocolVersion"])
	}

	c.Notify(t, "notifications/initialized", nil)
}

func TestToolsList(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/list", nil)
	if resp.Error != nil {
		t.Fatalf("tools/list returned error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	json.Unmarshal(resp.Result, &result)

	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = true
	}

	for _, expected := range []string{"request_command", "get_result", "list_requests", "register_container", "list_containers", "exec_container"} {
		if !toolNames[expected] {
			t.Errorf("expected tool %s not found", expected)
		}
	}
}

func TestRequestCommand(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"hello"},
			"reason":  "testing",
		},
	})

	if resp.Error != nil {
		t.Fatalf("request_command returned error: %s", resp.Error.Message)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)

	if len(result.Content) == 0 {
		t.Fatal("no content in response")
	}

	var parsed map[string]string
	json.Unmarshal([]byte(result.Content[0].Text), &parsed)

	if parsed["request_id"] == "" {
		t.Error("expected non-empty request_id")
	}
	if parsed["status"] != "pending" {
		t.Errorf("expected status pending, got %s", parsed["status"])
	}
}

func TestApprovalFlow(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Submit a command
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"hello", "world"},
			"reason":  "test approval flow",
		},
	})

	requestID := extractRequestID(t, resp)

	// Approve via web API
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("approve returned status %d", code)
	}

	// Poll for result
	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Status != "complete" {
		t.Errorf("expected status complete, got %s", result.Status)
	}
	if result.Result == nil {
		t.Fatal("expected result to be non-nil")
	}
	if !strings.Contains(result.Result.Stdout, "hello world") {
		t.Errorf("expected stdout to contain 'hello world', got %q", result.Result.Stdout)
	}
	if result.Result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.Result.ExitCode)
	}
}

func TestDenialFlow(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "rm",
			"args":    []string{"-rf", "/"},
			"reason":  "testing denial",
		},
	})

	requestID := extractRequestID(t, resp)

	// Deny via web API
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), requestID),
		s.token, map[string]string{"reason": "too dangerous"})
	if code != 200 {
		t.Fatalf("deny returned status %d", code)
	}

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Status != "denied" {
		t.Errorf("expected status denied, got %s", result.Status)
	}
	if result.DenyReason != "too dangerous" {
		t.Errorf("expected deny reason 'too dangerous', got %q", result.DenyReason)
	}
}

func TestShellMode(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Test shell mode with pipe
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo hello | tr a-z A-Z",
			"reason":  "test shell mode",
			"shell":   true,
		},
	})

	requestID := extractRequestID(t, resp)

	// Approve
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Status != "complete" {
		t.Errorf("expected status complete, got %s", result.Status)
	}
	if result.Result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Result.Stdout, "HELLO") {
		t.Errorf("expected HELLO in stdout, got %q", result.Result.Stdout)
	}
}

func TestDirectMode(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Direct mode — no shell
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"direct", "mode"},
			"reason":  "test direct mode",
		},
	})

	requestID := extractRequestID(t, resp)

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if !strings.Contains(result.Result.Stdout, "direct mode") {
		t.Errorf("expected 'direct mode' in stdout, got %q", result.Result.Stdout)
	}
}

func TestBlockingPoll(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"blocking"},
			"reason":  "test blocking poll",
		},
	})

	requestID := extractRequestID(t, resp)

	// Approve after a short delay
	go func() {
		time.Sleep(1 * time.Second)
		WebPost(t,
			fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
			s.token, nil)
	}()

	// Blocking poll — should wait until approved and executed
	start := time.Now()
	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
			"timeout":    10,
		},
	})
	elapsed := time.Since(start)

	result := extractResult(t, resultResp)
	// Should have waited at least ~1s for the approval
	if elapsed < 500*time.Millisecond {
		t.Errorf("blocking poll returned too quickly (%v)", elapsed)
	}
	if result.Status != "complete" && result.Status != "running" && result.Status != "approved" {
		// It might still be running or just approved when the poll returns
		if result.Status == "pending" {
			t.Error("expected non-pending status from blocking poll")
		}
	}
}

func TestAuthRequired(t *testing.T) {
	s := StartServer(t)

	// No token
	code, _ := WebGet(t, s.WebURL()+"/api/requests", "")
	if code != 401 {
		t.Errorf("expected 401 without token, got %d", code)
	}

	// Wrong token
	code, _ = WebGet(t, s.WebURL()+"/api/requests", "wrong-token")
	if code != 401 {
		t.Errorf("expected 401 with wrong token, got %d", code)
	}

	// Correct token
	code, _ = WebGet(t, s.WebURL()+"/api/requests", s.token)
	if code != 200 {
		t.Errorf("expected 200 with correct token, got %d", code)
	}
}

func TestDashboardNoAuth(t *testing.T) {
	s := StartServer(t)

	// Dashboard page itself should be accessible without auth (token entered client-side)
	code, _ := WebGet(t, s.WebURL()+"/", "")
	if code != 200 {
		t.Errorf("expected 200 for dashboard, got %d", code)
	}
}

func TestListRequestsFilter(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Create two requests
	c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"one"},
			"reason":  "first",
		},
	})

	resp2 := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"two"},
			"reason":  "second",
		},
	})

	requestID2 := extractRequestID(t, resp2)

	// Deny the second one
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), requestID2),
		s.token, map[string]string{"reason": "nope"})

	// List via MCP — all
	listResp := c.Call(t, 4, "tools/call", map[string]interface{}{
		"name":      "list_requests",
		"arguments": map[string]interface{}{},
	})

	allRequests := extractList(t, listResp)
	if len(allRequests) != 2 {
		t.Errorf("expected 2 requests, got %d", len(allRequests))
	}

	// List — pending only
	listResp = c.Call(t, 5, "tools/call", map[string]interface{}{
		"name": "list_requests",
		"arguments": map[string]interface{}{
			"status": "pending",
		},
	})

	pendingRequests := extractList(t, listResp)
	if len(pendingRequests) != 1 {
		t.Errorf("expected 1 pending request, got %d", len(pendingRequests))
	}
}

func TestAllowedDirs(t *testing.T) {
	s := StartServer(t, WithAllowedDirs("/tmp,/var"))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Command with disallowed working dir
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command":     "ls",
			"reason":      "test allowed dirs",
			"working_dir": "/etc",
		},
	})

	requestID := extractRequestID(t, resp)

	// Approve it
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Result == nil {
		t.Fatal("expected result")
	}
	// Should fail because /etc is not in allowed dirs
	if result.Result.ExitCode == 0 {
		t.Error("expected non-zero exit code for disallowed directory")
	}
	if !strings.Contains(result.Result.Stderr, "not in allowed directories") {
		t.Errorf("expected 'not in allowed directories' in stderr, got %q", result.Result.Stderr)
	}
}

func TestAllowedDirsPermitted(t *testing.T) {
	s := StartServer(t, WithAllowedDirs("/tmp"))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Command with allowed working dir
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command":     "echo",
			"args":        []string{"allowed"},
			"reason":      "test allowed dirs ok",
			"working_dir": "/tmp",
		},
	})

	requestID := extractRequestID(t, resp)

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Status != "complete" {
		t.Errorf("expected complete, got %s", result.Status)
	}
	if !strings.Contains(result.Result.Stdout, "allowed") {
		t.Errorf("expected 'allowed' in stdout, got %q", result.Result.Stdout)
	}
}

func TestDoubleApprove(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"once"},
			"reason":  "test double approve",
		},
	})

	requestID := extractRequestID(t, resp)

	// First approve
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("first approve returned %d", code)
	}

	time.Sleep(200 * time.Millisecond)

	// Second approve should fail (already approved)
	code, _ = WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 409 {
		t.Errorf("expected 409 for double approve, got %d", code)
	}
}

func TestPing(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	resp := c.Call(t, 1, "ping", nil)
	if resp.Error != nil {
		t.Fatalf("ping returned error: %s", resp.Error.Message)
	}
}

func TestAllowedDirsPrefixCollision(t *testing.T) {
	s := StartServer(t, WithAllowedDirs("/tmp"))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// /tmp-evil shares a prefix with /tmp but is NOT a subdirectory
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command":     "echo",
			"args":        []string{"pwned"},
			"reason":      "test prefix collision",
			"working_dir": "/tmp-evil",
		},
	})

	requestID := extractRequestID(t, resp)

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Result == nil {
		t.Fatal("expected result")
	}
	if result.Result.ExitCode == 0 {
		t.Error("expected non-zero exit code for /tmp-evil (prefix collision)")
	}
	if !strings.Contains(result.Result.Stderr, "not in allowed directories") {
		t.Errorf("expected 'not in allowed directories' in stderr, got %q", result.Result.Stderr)
	}
}

func TestAllowedDirsPathTraversal(t *testing.T) {
	s := StartServer(t, WithAllowedDirs("/tmp"))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Path traversal: /tmp/../../etc normalizes to /etc
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command":     "echo",
			"args":        []string{"traversed"},
			"reason":      "test path traversal",
			"working_dir": "/tmp/../../etc",
		},
	})

	requestID := extractRequestID(t, resp)

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Result == nil {
		t.Fatal("expected result")
	}
	if result.Result.ExitCode == 0 {
		t.Error("expected non-zero exit code for path traversal")
	}
	if !strings.Contains(result.Result.Stderr, "not in allowed directories") {
		t.Errorf("expected 'not in allowed directories' in stderr, got %q", result.Result.Stderr)
	}
}

func TestAllowedDirsSubdirPermitted(t *testing.T) {
	s := StartServer(t, WithAllowedDirs("/tmp"))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Create the subdir first
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "mkdir",
			"args":    []string{"-p", "/tmp/hr-test-subdir"},
			"reason":  "create test subdir",
		},
	})
	mkdirID := extractRequestID(t, resp)
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), mkdirID),
		s.token, nil)
	time.Sleep(500 * time.Millisecond)

	// Now use the subdir as working_dir
	resp = c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command":     "echo",
			"args":        []string{"subdir-ok"},
			"reason":      "test subdir permitted",
			"working_dir": "/tmp/hr-test-subdir",
		},
	})
	requestID := extractRequestID(t, resp)

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 4, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Status != "complete" {
		t.Errorf("expected complete, got %s", result.Status)
	}
	if result.Result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(result.Result.Stdout, "subdir-ok") {
		t.Errorf("expected 'subdir-ok' in stdout, got %q", result.Result.Stdout)
	}

	// Cleanup
	cleanResp := c.Call(t, 5, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "rmdir",
			"args":    []string{"/tmp/hr-test-subdir"},
			"reason":  "cleanup test subdir",
		},
	})
	cleanID := extractRequestID(t, cleanResp)
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), cleanID),
		s.token, nil)
}

func TestOutputTruncation(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Generate ~2.7MB of output (2048 * 1024 bytes of zeros, base64-encoded)
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "dd if=/dev/zero bs=1024 count=2048 | base64",
			"reason":  "test output truncation",
			"shell":   true,
		},
	})

	requestID := extractRequestID(t, resp)

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	// Give the large command time to execute
	time.Sleep(3 * time.Second)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
			"timeout":    10,
		},
	})

	result := extractResult(t, resultResp)
	if result.Status != "complete" {
		t.Fatalf("expected complete, got %s", result.Status)
	}
	if result.Result == nil {
		t.Fatal("expected result")
	}

	// Stdout should be capped at ~1MB (1048576 bytes)
	if len(result.Result.Stdout) > 1500000 {
		t.Errorf("expected stdout to be truncated, got %d bytes", len(result.Result.Stdout))
	}
	// Stderr should contain truncation notice
	if !strings.Contains(result.Result.Stderr, "[human-relay: stdout truncated at 1MB]") {
		t.Errorf("expected truncation notice in stderr, got %q", result.Result.Stderr)
	}
}

func TestNonAsciiCommandPreserved(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Submit a command with Cyrillic characters (homoglyph attack simulation)
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"hello-\u0430\u0441\u0446\u0438\u0438"},
			"reason":  "test non-ascii preservation",
		},
	})

	requestID := extractRequestID(t, resp)

	// Verify the request is stored correctly via list_requests
	listResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name":      "list_requests",
		"arguments": map[string]interface{}{},
	})

	allRequests := extractList(t, listResp)
	var found *RequestResult
	for i := range allRequests {
		if allRequests[i].ID == requestID {
			found = &allRequests[i]
			break
		}
	}
	if found == nil {
		t.Fatal("request not found in list")
	}
	if found.Command != "echo" {
		t.Errorf("expected command 'echo', got %q", found.Command)
	}
	if len(found.Args) == 0 || !strings.Contains(found.Args[0], "\u0430\u0441\u0446\u0438\u0438") {
		t.Errorf("expected Cyrillic characters in args, got %v", found.Args)
	}

	// Approve and execute to verify end-to-end pipeline
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 4, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Status != "complete" {
		t.Errorf("expected status complete, got %s", result.Status)
	}
	if result.Result == nil {
		t.Fatal("expected result")
	}
	// echo should output the Cyrillic characters faithfully
	if !strings.Contains(result.Result.Stdout, "\u0430\u0441\u0446\u0438\u0438") {
		t.Errorf("expected Cyrillic characters in stdout, got %q", result.Result.Stdout)
	}
}

func TestDashboardBase64DecodeFeature(t *testing.T) {
	s := StartServer(t)

	// Fetch dashboard HTML
	code, body := WebGet(t, s.WebURL()+"/", "")
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	html := string(body)

	// Verify the base64 decode JavaScript functions are present
	for _, fn := range []string{"tryDecodeBase64", "findBase64Blobs", "buildBase64Display", "toggleB64"} {
		if !strings.Contains(html, fn) {
			t.Errorf("expected dashboard HTML to contain function %q", fn)
		}
	}

	// Verify the CSS classes are present
	for _, cls := range []string{".b64-section", ".b64-label", ".b64-decoded"} {
		if !strings.Contains(html, cls) {
			t.Errorf("expected dashboard HTML to contain CSS class %q", cls)
		}
	}
}

func TestDashboardWhitelistFilter(t *testing.T) {
	s := StartServer(t)

	code, body := WebGet(t, s.WebURL()+"/", "")
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	html := string(body)

	// Verify the whitelist filter button is in the filter bar
	if !strings.Contains(html, `data-filter="whitelist"`) {
		t.Error("expected dashboard HTML to contain whitelist filter button")
	}
	if !strings.Contains(html, "filter-whitelist") {
		t.Error("expected dashboard HTML to contain filter-whitelist CSS class")
	}

	// Verify the filter divider separating whitelist from status filters
	if !strings.Contains(html, "filter-divider") {
		t.Error("expected dashboard HTML to contain filter-divider")
	}

	// Verify whitelist view rendering functions
	for _, fn := range []string{"renderWhitelistView", "updateWhitelistCount", "fetchWhitelist"} {
		if !strings.Contains(html, fn) {
			t.Errorf("expected dashboard HTML to contain function %q", fn)
		}
	}

	// Verify whitelist rule CSS classes
	for _, cls := range []string{".wl-rule", ".wl-cmd", ".btn-wl-remove", ".filter-divider"} {
		if !strings.Contains(html, cls) {
			t.Errorf("expected dashboard HTML to contain CSS class %q", cls)
		}
	}

	// Verify the old whitelist panel is gone
	if strings.Contains(html, "whitelistPanel") {
		t.Error("expected old whitelistPanel element to be removed")
	}
	if strings.Contains(html, "toggleWhitelistPanel") {
		t.Error("expected old toggleWhitelistPanel function to be removed")
	}
}

func TestBase64CommandPreserved(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Submit a shell command containing base64-encoded content (simulating a heredoc file write)
	b64Content := "Z2xvYmFsOgogIHNjcmFwZV9pbnRlcnZhbDogMTVz"
	shellCmd := "base64 -d > /tmp/test.yml <<'B64EOF'\n" + b64Content + "\nB64EOF"

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": shellCmd,
			"reason":  "test base64 command preservation",
			"shell":   true,
		},
	})

	requestID := extractRequestID(t, resp)

	// Verify the base64 content is preserved in the stored request
	listResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name":      "list_requests",
		"arguments": map[string]interface{}{},
	})

	allRequests := extractList(t, listResp)
	var found *RequestResult
	for i := range allRequests {
		if allRequests[i].ID == requestID {
			found = &allRequests[i]
			break
		}
	}
	if found == nil {
		t.Fatal("request not found in list")
	}

	// The command should contain the base64 blob intact
	if !strings.Contains(found.Command, b64Content) {
		t.Errorf("expected command to contain base64 content %q, got %q", b64Content, found.Command)
	}

	// Deny so it doesn't execute
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), requestID),
		s.token, map[string]string{"reason": "test only"})
}

// Helpers

type RequestResult struct {
	ID             string      `json:"id"`
	Command        string      `json:"command"`
	Args           []string    `json:"args"`
	Reason         string      `json:"reason"`
	Status         string      `json:"status"`
	CreatedAt      time.Time   `json:"created_at"`
	DenyReason     string      `json:"deny_reason"`
	WithdrawReason string      `json:"withdraw_reason"`
	Result         *ExecResult `json:"result"`
	StdinLen       int         `json:"stdin_len"`
	DisplayCommand string      `json:"display_command"`
}

type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func extractRequestID(t *testing.T, resp *JSONRPCResponse) string {
	t.Helper()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)

	if len(result.Content) == 0 {
		t.Fatal("no content in response")
	}

	var parsed map[string]string
	json.Unmarshal([]byte(result.Content[0].Text), &parsed)

	id := parsed["request_id"]
	if id == "" {
		t.Fatal("empty request_id")
	}
	return id
}

func extractResult(t *testing.T, resp *JSONRPCResponse) *RequestResult {
	t.Helper()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)

	if len(result.Content) == 0 {
		t.Fatal("no content in response")
	}

	var parsed RequestResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsed); err != nil {
		t.Fatalf("failed to parse result: %v\nraw: %s", err, result.Content[0].Text)
	}
	return &parsed
}

func extractList(t *testing.T, resp *JSONRPCResponse) []RequestResult {
	t.Helper()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)

	if len(result.Content) == 0 {
		t.Fatal("no content in response")
	}

	var parsed []RequestResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &parsed); err != nil {
		t.Fatalf("failed to parse list: %v\nraw: %s", err, result.Content[0].Text)
	}
	return parsed
}
