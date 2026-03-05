package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"testing"
	"time"
)

func TestCooldownBlocksSecondApproval(t *testing.T) {
	s := StartServer(t, WithCooldown(3))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Create two requests
	resp1 := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"first"},
			"reason":  "cooldown test 1",
		},
	})
	id1 := extractRequestID(t, resp1)

	resp2 := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"second"},
			"reason":  "cooldown test 2",
		},
	})
	id2 := extractRequestID(t, resp2)

	// Approve first — should succeed
	code1, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id1),
		s.token, nil)
	if code1 != 200 {
		t.Fatalf("first approve: expected 200, got %d", code1)
	}

	// Approve second immediately — should be blocked
	code2, body2 := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id2),
		s.token, nil)
	if code2 != 429 {
		t.Fatalf("second approve: expected 429, got %d", code2)
	}

	var errResp map[string]interface{}
	json.Unmarshal(body2, &errResp)
	if errResp["error"] != "cooldown active" {
		t.Fatalf("expected cooldown error, got %v", errResp)
	}
	remainMs, ok := errResp["remaining_ms"].(float64)
	if !ok || remainMs <= 0 {
		t.Fatalf("expected positive remaining_ms, got %v", errResp["remaining_ms"])
	}
}

func TestCooldownAllowsAfterExpiry(t *testing.T) {
	s := StartServer(t, WithCooldown(1)) // 1-second cooldown
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp1 := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"first"},
			"reason":  "cooldown expiry test 1",
		},
	})
	id1 := extractRequestID(t, resp1)

	// Approve first
	code1, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id1),
		s.token, nil)
	if code1 != 200 {
		t.Fatalf("first approve: expected 200, got %d", code1)
	}

	// Wait for cooldown to expire
	time.Sleep(1100 * time.Millisecond)

	// Second approval should now succeed
	resp2 := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"second"},
			"reason":  "cooldown expiry test 2",
		},
	})
	id2 := extractRequestID(t, resp2)

	code2, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id2),
		s.token, nil)
	if code2 != 200 {
		t.Fatalf("second approve after expiry: expected 200, got %d", code2)
	}
}

func TestCooldownDoesNotBlockDeny(t *testing.T) {
	s := StartServer(t, WithCooldown(3))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp1 := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"first"},
			"reason":  "cooldown deny test 1",
		},
	})
	id1 := extractRequestID(t, resp1)

	resp2 := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"second"},
			"reason":  "cooldown deny test 2",
		},
	})
	id2 := extractRequestID(t, resp2)

	// Approve first to start cooldown
	code1, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id1),
		s.token, nil)
	if code1 != 200 {
		t.Fatalf("approve: expected 200, got %d", code1)
	}

	// Deny during cooldown — should work
	code2, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), id2),
		s.token, nil)
	if code2 != 200 {
		t.Fatalf("deny during cooldown: expected 200, got %d", code2)
	}
}

func TestListCooldownHeader(t *testing.T) {
	s := StartServer(t, WithCooldown(3))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Before any approval — cooldown should be 0
	resp1 := WebGetResp(t, s.WebURL()+"/api/requests", s.token)
	io.ReadAll(resp1.Body)
	resp1.Body.Close()

	cdHeader := resp1.Header.Get("X-Cooldown-Remaining-Ms")
	if cdHeader == "" {
		t.Fatal("expected X-Cooldown-Remaining-Ms header")
	}
	cdMs, _ := strconv.Atoi(cdHeader)
	if cdMs != 0 {
		t.Fatalf("expected 0 cooldown before any approval, got %d", cdMs)
	}

	// Approve a request
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"hello"},
			"reason":  "cooldown header test",
		},
	})
	id := extractRequestID(t, resp)

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id),
		s.token, nil)

	// Now list should show remaining cooldown > 0
	resp2 := WebGetResp(t, s.WebURL()+"/api/requests", s.token)
	io.ReadAll(resp2.Body)
	resp2.Body.Close()

	cdMs2, _ := strconv.Atoi(resp2.Header.Get("X-Cooldown-Remaining-Ms"))
	if cdMs2 <= 0 {
		t.Fatalf("expected positive cooldown after approval, got %d", cdMs2)
	}
	if cdMs2 > 3000 {
		t.Fatalf("cooldown should not exceed 3000ms (configured 3s), got %d", cdMs2)
	}
}

func TestListCooldownHeaderZeroAfterExpiry(t *testing.T) {
	s := StartServer(t, WithCooldown(1))
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Approve a request
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"hello"},
			"reason":  "cooldown expiry header test",
		},
	})
	id := extractRequestID(t, resp)

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id),
		s.token, nil)

	// Wait for cooldown to expire
	time.Sleep(1100 * time.Millisecond)

	resp2 := WebGetResp(t, s.WebURL()+"/api/requests", s.token)
	io.ReadAll(resp2.Body)
	resp2.Body.Close()

	cdMs, _ := strconv.Atoi(resp2.Header.Get("X-Cooldown-Remaining-Ms"))
	if cdMs != 0 {
		t.Fatalf("expected 0 after cooldown expiry, got %d", cdMs)
	}
}
