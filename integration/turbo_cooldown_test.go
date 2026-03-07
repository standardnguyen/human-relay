package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"testing"
	"time"
)

// Helper: create an MCP client with initialized session
func setupMCP(t *testing.T, s *TestServer) *MCPClient {
	t.Helper()
	c := NewMCPClient(t, s.MCPURL())
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)
	return c
}

// Helper: create a pending request and return its ID
func mkRequest(t *testing.T, c *MCPClient, callID int, reason string) string {
	t.Helper()
	resp := c.Call(t, callID, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"test"},
			"reason":  reason,
		},
	})
	return extractRequestID(t, resp)
}

// Helper: activate turbo via web API
func activateTurbo(t *testing.T, s *TestServer, durationMin, cooldownSec int) {
	t.Helper()
	code, body := WebPost(t,
		s.WebURL()+"/api/turbocharge",
		s.token,
		map[string]interface{}{
			"duration_minutes": durationMin,
			"cooldown_seconds": cooldownSec,
		},
	)
	if code != 200 {
		t.Fatalf("turbo activate: expected 200, got %d: %s", code, body)
	}
}

// Helper: get cooldown remaining from list headers
func getCooldownHeaders(t *testing.T, s *TestServer) (remainMs, durationMs int, turboActive bool) {
	t.Helper()
	resp := WebGetResp(t, s.WebURL()+"/api/requests", s.token)
	io.ReadAll(resp.Body)
	resp.Body.Close()

	remainMs, _ = strconv.Atoi(resp.Header.Get("X-Cooldown-Remaining-Ms"))
	durationMs, _ = strconv.Atoi(resp.Header.Get("X-Cooldown-Duration-Ms"))
	turboActive = resp.Header.Get("X-Turbo-Active") == "true"
	return
}

// When turbo is activated mid-cooldown and the turbo cooldown has already
// elapsed since last approval, the server should return 0 remaining.
// This is the core bug scenario: approve with 5s cooldown, wait 2s,
// activate turbo with 1s cooldown -> remaining should be 0.
func TestTurboMidCooldown_ElapsedBeyondTurboCooldown(t *testing.T) {
	s := StartServer(t, WithCooldown(5))
	c := setupMCP(t, s)

	id1 := mkRequest(t, c, 2, "turbo mid-cooldown elapsed test")

	// Approve to start 5s cooldown
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id1),
		s.token, nil)
	if code != 200 {
		t.Fatalf("approve: expected 200, got %d", code)
	}

	// Wait 2s so elapsed > turbo cooldown (1s)
	time.Sleep(2 * time.Second)

	// Activate turbo with 1s cooldown
	activateTurbo(t, s, 5, 1)

	// Server should report 0 remaining (2s elapsed > 1s turbo cooldown)
	remainMs, durationMs, turboActive := getCooldownHeaders(t, s)
	if !turboActive {
		t.Fatal("expected turbo to be active")
	}
	if remainMs != 0 {
		t.Fatalf("expected 0 remaining (elapsed > turbo cooldown), got %d", remainMs)
	}
	if durationMs != 1000 {
		t.Fatalf("expected cooldown duration 1000ms, got %d", durationMs)
	}
}

// When turbo is activated mid-cooldown and elapsed < turbo cooldown,
// the remaining time should be shortened (not zero).
func TestTurboMidCooldown_ShortenedRemaining(t *testing.T) {
	s := StartServer(t, WithCooldown(5))
	c := setupMCP(t, s)

	id1 := mkRequest(t, c, 2, "turbo mid-cooldown shortened test")

	// Approve to start 5s cooldown
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id1),
		s.token, nil)
	if code != 200 {
		t.Fatalf("approve: expected 200, got %d", code)
	}

	// Immediately activate turbo with 3s cooldown (elapsed ~0)
	activateTurbo(t, s, 5, 3)

	// Should show shortened remaining (~3s instead of ~5s)
	remainMs, durationMs, turboActive := getCooldownHeaders(t, s)
	if !turboActive {
		t.Fatal("expected turbo to be active")
	}
	if durationMs != 3000 {
		t.Fatalf("expected cooldown duration 3000ms, got %d", durationMs)
	}
	// Remaining should be > 0 but <= 3000 (not the original ~5000)
	if remainMs <= 0 {
		t.Fatalf("expected positive remaining, got %d", remainMs)
	}
	if remainMs > 3100 {
		t.Fatalf("expected remaining <= 3100ms (turbo 3s), got %d", remainMs)
	}
}

// After turbo shortens cooldown past elapsed time, a second approval
// should succeed immediately (server-side check).
func TestTurboMidCooldown_AllowsApprovalAfterTurboExpires(t *testing.T) {
	s := StartServer(t, WithCooldown(5))
	c := setupMCP(t, s)

	id1 := mkRequest(t, c, 2, "turbo allows approval test 1")
	id2 := mkRequest(t, c, 3, "turbo allows approval test 2")

	// Approve first to start 5s cooldown
	code1, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id1),
		s.token, nil)
	if code1 != 200 {
		t.Fatalf("first approve: expected 200, got %d", code1)
	}

	// Without turbo, second approval should fail (within 5s cooldown)
	code2, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id2),
		s.token, nil)
	if code2 != 429 {
		t.Fatalf("second approve without turbo: expected 429, got %d", code2)
	}

	// Wait 2s, then activate turbo with 1s cooldown
	time.Sleep(2 * time.Second)
	activateTurbo(t, s, 5, 1)

	// Now approval should succeed (2s elapsed > 1s turbo cooldown)
	code3, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id2),
		s.token, nil)
	if code3 != 200 {
		t.Fatalf("approve after turbo: expected 200, got %d", code3)
	}
}

// Turbo activation + deactivation restores original cooldown.
func TestTurboDeactivation_RestoresOriginalCooldown(t *testing.T) {
	s := StartServer(t, WithCooldown(5))
	c := setupMCP(t, s)

	// Activate turbo
	activateTurbo(t, s, 5, 1)

	id1 := mkRequest(t, c, 2, "turbo deactivation test 1")
	id2 := mkRequest(t, c, 3, "turbo deactivation test 2")

	// Approve first (turbo active, 1s cooldown)
	code1, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id1),
		s.token, nil)
	if code1 != 200 {
		t.Fatalf("first approve: expected 200, got %d", code1)
	}

	// Deactivate turbo
	code, _ := WebDelete(t, s.WebURL()+"/api/turbocharge", s.token)
	if code != 200 {
		t.Fatalf("turbo deactivate: expected 200, got %d", code)
	}

	// Immediately try second approval — should be blocked (back to 5s cooldown)
	code2, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id2),
		s.token, nil)
	if code2 != 429 {
		t.Fatalf("approve after turbo deactivation: expected 429, got %d", code2)
	}

	// Headers should show original cooldown
	_, durationMs, turboActive := getCooldownHeaders(t, s)
	if turboActive {
		t.Fatal("expected turbo to be inactive")
	}
	if durationMs != 5000 {
		t.Fatalf("expected original cooldown 5000ms, got %d", durationMs)
	}
}

// Turbo GET endpoint returns correct state.
func TestTurboGetEndpoint(t *testing.T) {
	s := StartServer(t, WithCooldown(3))

	// Before activation
	code1, body1 := WebGet(t, s.WebURL()+"/api/turbocharge", s.token)
	if code1 != 200 {
		t.Fatalf("turbo GET: expected 200, got %d", code1)
	}
	var state1 map[string]interface{}
	json.Unmarshal(body1, &state1)
	if state1["active"] != false {
		t.Fatalf("expected turbo inactive, got %v", state1)
	}

	// Activate
	activateTurbo(t, s, 5, 2)

	code2, body2 := WebGet(t, s.WebURL()+"/api/turbocharge", s.token)
	if code2 != 200 {
		t.Fatalf("turbo GET after activate: expected 200, got %d", code2)
	}
	var state2 map[string]interface{}
	json.Unmarshal(body2, &state2)
	if state2["active"] != true {
		t.Fatalf("expected turbo active, got %v", state2)
	}
	if state2["cooldown_seconds"] != float64(2) {
		t.Fatalf("expected cooldown_seconds 2, got %v", state2["cooldown_seconds"])
	}
	remainMs := state2["remaining_ms"].(float64)
	if remainMs <= 0 || remainMs > 300000 {
		t.Fatalf("expected positive remaining within 5min, got %v", remainMs)
	}

	// Deactivate
	WebDelete(t, s.WebURL()+"/api/turbocharge", s.token)

	code3, body3 := WebGet(t, s.WebURL()+"/api/turbocharge", s.token)
	if code3 != 200 {
		t.Fatalf("turbo GET after deactivate: expected 200, got %d", code3)
	}
	var state3 map[string]interface{}
	json.Unmarshal(body3, &state3)
	if state3["active"] != false {
		t.Fatalf("expected turbo inactive after deactivate, got %v", state3)
	}
}

// Multiple approvals with turbo active should each respect the turbo cooldown.
func TestTurboRapidApprovals(t *testing.T) {
	s := StartServer(t, WithCooldown(5))
	c := setupMCP(t, s)

	// Activate turbo with 1s cooldown
	activateTurbo(t, s, 5, 1)

	id1 := mkRequest(t, c, 2, "turbo rapid 1")
	id2 := mkRequest(t, c, 3, "turbo rapid 2")
	id3 := mkRequest(t, c, 4, "turbo rapid 3")

	// Approve first
	code1, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id1),
		s.token, nil)
	if code1 != 200 {
		t.Fatalf("first approve: expected 200, got %d", code1)
	}

	// Immediately try second — should be blocked (within 1s turbo cooldown)
	code2, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id2),
		s.token, nil)
	if code2 != 429 {
		t.Fatalf("second approve immediately: expected 429, got %d", code2)
	}

	// Wait for turbo cooldown
	time.Sleep(1100 * time.Millisecond)

	// Now second should succeed
	code2b, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id2),
		s.token, nil)
	if code2b != 200 {
		t.Fatalf("second approve after turbo cooldown: expected 200, got %d", code2b)
	}

	// Immediately try third — blocked again
	code3, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id3),
		s.token, nil)
	if code3 != 429 {
		t.Fatalf("third approve immediately: expected 429, got %d", code3)
	}

	// Wait again
	time.Sleep(1100 * time.Millisecond)

	// Third should succeed
	code3b, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id3),
		s.token, nil)
	if code3b != 200 {
		t.Fatalf("third approve after turbo cooldown: expected 200, got %d", code3b)
	}
}
