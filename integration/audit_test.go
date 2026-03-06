package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type AuditEntry struct {
	Timestamp string                 `json:"timestamp"`
	Event     string                 `json:"event"`
	RequestID string                 `json:"request_id"`
	Fields    map[string]interface{} `json:"fields"`
}

func readAuditLog(t *testing.T, path string) []AuditEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()

	var entries []AuditEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Fatalf("parse audit entry: %v\nline: %s", err, scanner.Text())
		}
		entries = append(entries, e)
	}
	return entries
}

func TestAuditLogApprovalFlow(t *testing.T) {
	dataDir := t.TempDir()
	s := StartServer(t, WithDataDir(dataDir))
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
			"args":    []string{"audit-test"},
			"reason":  "testing audit log",
		},
	})
	requestID := extractRequestID(t, resp)

	// Approve
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("approve returned status %d", code)
	}

	// Wait for execution
	time.Sleep(500 * time.Millisecond)

	// Read audit log
	auditPath := filepath.Join(dataDir, "audit.log")
	entries := readAuditLog(t, auditPath)

	// Should have: request_created, request_approved, execution_started, execution_completed
	if len(entries) < 4 {
		t.Fatalf("expected at least 4 audit entries, got %d", len(entries))
	}

	events := make([]string, len(entries))
	for i, e := range entries {
		events[i] = e.Event
	}

	expected := []string{"request_created", "request_approved", "execution_started", "execution_completed"}
	for _, exp := range expected {
		found := false
		for _, ev := range events {
			if ev == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected event %q not found in audit log: %v", exp, events)
		}
	}

	// Verify request_created has the right fields
	created := entries[0]
	if created.RequestID != requestID {
		t.Errorf("expected request_id %s, got %s", requestID, created.RequestID)
	}
	if created.Fields["command"] != "echo" {
		t.Errorf("expected command 'echo', got %v", created.Fields["command"])
	}
	if created.Fields["reason"] != "testing audit log" {
		t.Errorf("expected reason 'testing audit log', got %v", created.Fields["reason"])
	}

	// Verify execution_completed has exit code and output
	completed := entries[len(entries)-1]
	if completed.Event != "execution_completed" {
		t.Fatalf("expected last entry to be execution_completed, got %s", completed.Event)
	}
	exitCode, ok := completed.Fields["exit_code"].(float64)
	if !ok || exitCode != 0 {
		t.Errorf("expected exit_code 0, got %v", completed.Fields["exit_code"])
	}
	stdout, _ := completed.Fields["stdout"].(string)
	if stdout == "" {
		t.Error("expected non-empty stdout in audit log")
	}
}

func TestAuditLogDenialFlow(t *testing.T) {
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
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "rm",
			"args":    []string{"-rf", "/"},
			"reason":  "bad idea",
		},
	})
	requestID := extractRequestID(t, resp)

	// Deny
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), requestID),
		s.token, map[string]string{"reason": "absolutely not"})
	if code != 200 {
		t.Fatalf("deny returned status %d", code)
	}

	// Read audit log
	auditPath := filepath.Join(dataDir, "audit.log")
	entries := readAuditLog(t, auditPath)

	// Should have: request_created, request_denied
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 audit entries, got %d", len(entries))
	}

	denied := entries[1]
	if denied.Event != "request_denied" {
		t.Fatalf("expected request_denied, got %s", denied.Event)
	}
	if denied.RequestID != requestID {
		t.Errorf("expected request_id %s, got %s", requestID, denied.RequestID)
	}
	if denied.Fields["deny_reason"] != "absolutely not" {
		t.Errorf("expected deny_reason 'absolutely not', got %v", denied.Fields["deny_reason"])
	}
}
