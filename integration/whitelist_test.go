package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWhitelistAutoApprove(t *testing.T) {
	// Create a whitelist file that auto-approves "echo hello"
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "echo", "args": []string{"hello"}},
	}
	data, _ := json.Marshal(rules)
	os.WriteFile(wlPath, data, 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Submit a whitelisted command
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"hello"},
			"reason":  "test whitelist auto-approve",
		},
	})

	requestID := extractRequestID(t, resp)

	// Should auto-approve and complete without manual approval
	// Poll with timeout — the command should complete on its own
	var result *RequestResult
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
			"name": "get_result",
			"arguments": map[string]interface{}{
				"request_id": requestID,
			},
		})
		result = extractResult(t, resultResp)
		if result.Status == "complete" || result.Status == "error" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if result.Status != "complete" {
		t.Fatalf("expected auto-approved command to complete, got status %s", result.Status)
	}
	if result.Result == nil {
		t.Fatal("expected result to be non-nil")
	}
	if result.Result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.Result.ExitCode)
	}
	if result.Result.Stdout != "hello\n" {
		t.Errorf("expected stdout 'hello\\n', got %q", result.Result.Stdout)
	}
}

func TestWhitelistNoMatchRequiresApproval(t *testing.T) {
	// Whitelist only "echo hello"
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "echo", "args": []string{"hello"}},
	}
	data, _ := json.Marshal(rules)
	os.WriteFile(wlPath, data, 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Submit a NON-whitelisted command (different args)
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"goodbye"},
			"reason":  "test non-whitelisted command",
		},
	})

	requestID := extractRequestID(t, resp)

	// Wait briefly and verify it stays pending (not auto-approved)
	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result := extractResult(t, resultResp)
	if result.Status != "pending" {
		t.Errorf("expected non-whitelisted command to stay pending, got %s", result.Status)
	}

	// Now manually approve to confirm normal flow still works
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("approve returned status %d", code)
	}

	time.Sleep(500 * time.Millisecond)

	resultResp = c.Call(t, 4, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})

	result = extractResult(t, resultResp)
	if result.Status != "complete" {
		t.Errorf("expected complete after manual approval, got %s", result.Status)
	}
}

func TestWhitelistNoCooldown(t *testing.T) {
	// Verify that whitelisted auto-approvals don't trigger the cooldown timer
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "echo", "args": []string{"fast"}},
	}
	data, _ := json.Marshal(rules)
	os.WriteFile(wlPath, data, 0644)

	// Use a long cooldown so we'd notice if whitelist triggers it
	s := StartServer(t, WithWhitelistFile(wlPath), WithCooldown(60))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Fire two whitelisted commands back-to-back
	for i := 0; i < 2; i++ {
		resp := c.Call(t, 2+i, "tools/call", map[string]interface{}{
			"name": "request_command",
			"arguments": map[string]interface{}{
				"command": "echo",
				"args":    []string{"fast"},
				"reason":  fmt.Sprintf("whitelist cooldown test %d", i),
			},
		})
		requestID := extractRequestID(t, resp)

		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resultResp := c.Call(t, 10+i, "tools/call", map[string]interface{}{
				"name": "get_result",
				"arguments": map[string]interface{}{
					"request_id": requestID,
				},
			})
			result := extractResult(t, resultResp)
			if result.Status == "complete" {
				break
			}
			if result.Status == "pending" && time.Now().After(deadline.Add(-1*time.Second)) {
				t.Fatalf("command %d still pending — cooldown may be blocking whitelist", i)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	// Now submit a non-whitelisted command and manually approve it
	// (verifying that the cooldown wasn't consumed by the whitelisted commands)
	resp := c.Call(t, 20, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"manual"},
			"reason":  "manual after whitelist",
		},
	})
	requestID := extractRequestID(t, resp)

	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("manual approve after whitelist returned %d (cooldown may have been triggered)", code)
	}
}

func TestWhitelistEmptyFile(t *testing.T) {
	// Empty whitelist file should work (no auto-approvals)
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	os.WriteFile(wlPath, []byte(`[]`), 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))
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
			"reason":  "empty whitelist test",
		},
	})

	requestID := extractRequestID(t, resp)
	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})
	result := extractResult(t, resultResp)
	if result.Status != "pending" {
		t.Errorf("expected pending with empty whitelist, got %s", result.Status)
	}
}

func TestWhitelistMissingFile(t *testing.T) {
	// Non-existent whitelist file should start fine (no auto-approvals)
	s := StartServer(t, WithWhitelistFile("/nonexistent/whitelist.json"))
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
			"reason":  "missing whitelist test",
		},
	})

	requestID := extractRequestID(t, resp)
	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})
	result := extractResult(t, resultResp)
	if result.Status != "pending" {
		t.Errorf("expected pending with missing whitelist file, got %s", result.Status)
	}
}

func TestWhitelistAPIList(t *testing.T) {
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "echo", "args": []string{"hello"}},
		{"command": "ls", "args": []string{"-la"}},
	}
	data, _ := json.Marshal(rules)
	os.WriteFile(wlPath, data, 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))

	code, body := WebGet(t, s.WebURL()+"/api/whitelist", s.token)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	var got []map[string]interface{}
	json.Unmarshal(body, &got)
	if len(got) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(got))
	}
	if got[0]["command"] != "echo" {
		t.Errorf("expected first rule command 'echo', got %v", got[0]["command"])
	}
}

func TestWhitelistAPIAdd(t *testing.T) {
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	os.WriteFile(wlPath, []byte(`[]`), 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Add a rule via API
	code, _ := WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
		"command": "echo",
		"args":    []string{"hello"},
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	// Verify it appears in list
	code, body := WebGet(t, s.WebURL()+"/api/whitelist", s.token)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	var got []map[string]interface{}
	json.Unmarshal(body, &got)
	if len(got) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(got))
	}

	// Now submit a command matching the new rule — should auto-approve
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"hello"},
			"reason":  "test dynamic whitelist",
		},
	})
	requestID := extractRequestID(t, resp)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
			"name": "get_result",
			"arguments": map[string]interface{}{
				"request_id": requestID,
			},
		})
		result := extractResult(t, resultResp)
		if result.Status == "complete" {
			return // success
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("dynamically added whitelist rule did not auto-approve")
}

func TestWhitelistAPIRemove(t *testing.T) {
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	rules := []map[string]interface{}{
		{"command": "echo", "args": []string{"hello"}},
	}
	data, _ := json.Marshal(rules)
	os.WriteFile(wlPath, data, 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Remove the rule via API
	code, _ := WebPost(t, s.WebURL()+"/api/whitelist/remove", s.token, map[string]interface{}{
		"command": "echo",
		"args":    []string{"hello"},
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	// Verify it's gone
	code, body := WebGet(t, s.WebURL()+"/api/whitelist", s.token)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	var got []map[string]interface{}
	json.Unmarshal(body, &got)
	if len(got) != 0 {
		t.Fatalf("expected 0 rules after remove, got %d", len(got))
	}

	// Submit a command that was previously whitelisted — should stay pending now
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"hello"},
			"reason":  "test whitelist removal",
		},
	})
	requestID := extractRequestID(t, resp)

	time.Sleep(500 * time.Millisecond)

	resultResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "get_result",
		"arguments": map[string]interface{}{
			"request_id": requestID,
		},
	})
	result := extractResult(t, resultResp)
	if result.Status != "pending" {
		t.Errorf("expected pending after whitelist removal, got %s", result.Status)
	}
}

func TestWhitelistAPIAddDuplicate(t *testing.T) {
	dir := t.TempDir()
	wlPath := filepath.Join(dir, "whitelist.json")
	os.WriteFile(wlPath, []byte(`[]`), 0644)

	s := StartServer(t, WithWhitelistFile(wlPath))

	// Add same rule twice
	WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
		"command": "echo",
		"args":    []string{"hello"},
	})
	WebPost(t, s.WebURL()+"/api/whitelist", s.token, map[string]interface{}{
		"command": "echo",
		"args":    []string{"hello"},
	})

	// Should only have one rule
	_, body := WebGet(t, s.WebURL()+"/api/whitelist", s.token)
	var got []map[string]interface{}
	json.Unmarshal(body, &got)
	if len(got) != 1 {
		t.Errorf("expected 1 rule (no duplicates), got %d", len(got))
	}
}

func TestWhitelistAPIRequiresAuth(t *testing.T) {
	s := StartServer(t)

	code, _ := WebGet(t, s.WebURL()+"/api/whitelist", "")
	if code != 401 {
		t.Errorf("expected 401 without token, got %d", code)
	}

	code, _ = WebPost(t, s.WebURL()+"/api/whitelist", "", map[string]interface{}{
		"command": "echo",
		"args":    []string{"hello"},
	})
	if code != 401 {
		t.Errorf("expected 401 for add without token, got %d", code)
	}

	code, _ = WebPost(t, s.WebURL()+"/api/whitelist/remove", "", map[string]interface{}{
		"command": "echo",
		"args":    []string{"hello"},
	})
	if code != 401 {
		t.Errorf("expected 401 for remove without token, got %d", code)
	}
}
