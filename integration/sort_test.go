package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// webListRequests fetches requests from the web API with an optional status filter.
func webListRequests(t *testing.T, s *TestServer, status string) []RequestResult {
	t.Helper()
	url := s.WebURL() + "/api/requests"
	if status != "" {
		url += "?status=" + status
	}
	code, body := WebGet(t, url, s.token)
	if code != 200 {
		t.Fatalf("GET %s returned %d: %s", url, code, body)
	}
	var out []RequestResult
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("failed to parse list: %v", err)
	}
	return out
}

// createRequest creates a request via MCP and returns its ID.
func createRequest(t *testing.T, c *MCPClient, id int, reason string) string {
	t.Helper()
	resp := c.Call(t, id, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{reason},
			"reason":  reason,
		},
	})
	return extractRequestID(t, resp)
}

func TestSortCompletedNewestFirst(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Create 3 requests and approve them sequentially so they complete in order
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		ids[i] = createRequest(t, c, 10+i, fmt.Sprintf("req-%d", i))
		time.Sleep(50 * time.Millisecond) // ensure distinct timestamps
	}

	// Approve all three
	for _, id := range ids {
		WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id), s.token, nil)
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for completion
	time.Sleep(500 * time.Millisecond)

	list := webListRequests(t, s, "complete")
	if len(list) != 3 {
		t.Fatalf("expected 3 complete requests, got %d", len(list))
	}

	// Should be newest first: ids[2], ids[1], ids[0]
	for i := 0; i < len(list)-1; i++ {
		if !list[i].CreatedAt.After(list[i+1].CreatedAt) && !list[i].CreatedAt.Equal(list[i+1].CreatedAt) {
			t.Errorf("complete list not newest-first: [%d] %v <= [%d] %v",
				i, list[i].CreatedAt, i+1, list[i+1].CreatedAt)
		}
	}
	if list[0].ID != ids[2] || list[2].ID != ids[0] {
		t.Errorf("expected order [%s, %s, %s], got [%s, %s, %s]",
			ids[2], ids[1], ids[0], list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestSortDeniedNewestFirst(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		ids[i] = createRequest(t, c, 10+i, fmt.Sprintf("deny-%d", i))
		time.Sleep(50 * time.Millisecond)
	}

	// Deny all three
	for _, id := range ids {
		WebPost(t, fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), id),
			s.token, map[string]string{"reason": "test"})
	}

	list := webListRequests(t, s, "denied")
	if len(list) != 3 {
		t.Fatalf("expected 3 denied requests, got %d", len(list))
	}

	// Should be newest first
	if list[0].ID != ids[2] || list[2].ID != ids[0] {
		t.Errorf("denied not newest-first: expected [%s, %s, %s], got [%s, %s, %s]",
			ids[2], ids[1], ids[0], list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestSortErrorNewestFirst(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Create requests that will error (false returns exit code 1)
	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		resp := c.Call(t, 10+i, "tools/call", map[string]interface{}{
			"name": "request_command",
			"arguments": map[string]interface{}{
				"command": "false",
				"reason":  fmt.Sprintf("error-%d", i),
			},
		})
		ids[i] = extractRequestID(t, resp)
		time.Sleep(50 * time.Millisecond)
	}

	for _, id := range ids {
		WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id), s.token, nil)
		time.Sleep(200 * time.Millisecond)
	}

	time.Sleep(500 * time.Millisecond)

	list := webListRequests(t, s, "error")
	if len(list) != 3 {
		t.Fatalf("expected 3 error requests, got %d", len(list))
	}

	// Should be newest first
	if list[0].ID != ids[2] || list[2].ID != ids[0] {
		t.Errorf("error not newest-first: expected [%s, %s, %s], got [%s, %s, %s]",
			ids[2], ids[1], ids[0], list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestSortPendingOldestFirst(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	ids := make([]string, 3)
	for i := 0; i < 3; i++ {
		ids[i] = createRequest(t, c, 10+i, fmt.Sprintf("pending-%d", i))
		time.Sleep(50 * time.Millisecond)
	}

	list := webListRequests(t, s, "pending")
	if len(list) != 3 {
		t.Fatalf("expected 3 pending requests, got %d", len(list))
	}

	// Should be oldest first
	if list[0].ID != ids[0] || list[2].ID != ids[2] {
		t.Errorf("pending not oldest-first: expected [%s, %s, %s], got [%s, %s, %s]",
			ids[0], ids[1], ids[2], list[0].ID, list[1].ID, list[2].ID)
	}
}

func TestSortAllPendingFirstThenNewestFirst(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())

	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	// Create 5 requests with staggered timestamps
	ids := make([]string, 5)
	for i := 0; i < 5; i++ {
		ids[i] = createRequest(t, c, 10+i, fmt.Sprintf("mixed-%d", i))
		time.Sleep(50 * time.Millisecond)
	}

	// Complete ids[0] (oldest) and ids[2] (middle)
	WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), ids[0]), s.token, nil)
	time.Sleep(300 * time.Millisecond)
	WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), ids[2]), s.token, nil)
	time.Sleep(300 * time.Millisecond)

	// Deny ids[4] (newest)
	WebPost(t, fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), ids[4]),
		s.token, map[string]string{"reason": "test"})

	// ids[1] and ids[3] remain pending
	time.Sleep(300 * time.Millisecond)

	list := webListRequests(t, s, "")
	if len(list) != 5 {
		t.Fatalf("expected 5 requests, got %d", len(list))
	}

	// Expected order:
	// 1. Pending items oldest-first: ids[1], ids[3]
	// 2. Non-pending newest-first: ids[4] (denied), ids[2] (complete), ids[0] (complete)
	if list[0].ID != ids[1] {
		t.Errorf("position 0: expected pending %s, got %s (status=%s)", ids[1], list[0].ID, list[0].Status)
	}
	if list[1].ID != ids[3] {
		t.Errorf("position 1: expected pending %s, got %s (status=%s)", ids[3], list[1].ID, list[1].Status)
	}

	// Verify pending items are in the first two positions
	if list[0].Status != "pending" || list[1].Status != "pending" {
		t.Errorf("expected first two items to be pending, got %s and %s", list[0].Status, list[1].Status)
	}

	// Verify non-pending items are newest-first
	for i := 2; i < len(list)-1; i++ {
		if list[i].Status == "pending" {
			t.Errorf("position %d should not be pending", i)
		}
		if list[i].CreatedAt.Before(list[i+1].CreatedAt) {
			t.Errorf("non-pending not newest-first: [%d] %v < [%d] %v",
				i, list[i].CreatedAt, i+1, list[i+1].CreatedAt)
		}
	}
}
