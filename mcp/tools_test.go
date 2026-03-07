package mcp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"git.ekaterina.net/administrator/human-relay/audit"
	"git.ekaterina.net/administrator/human-relay/containers"
	"git.ekaterina.net/administrator/human-relay/store"
)

func setup(t *testing.T) *ToolHandler {
	t.Helper()
	s := store.New()
	cs, err := containers.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	al, err := audit.NewLogger(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	t.Cleanup(func() { al.Close() })
	return NewToolHandler(s, cs, "192.168.10.50", al)
}

func TestRegisterContainer(t *testing.T) {
	h := setup(t)

	result := h.Handle("register_container", map[string]interface{}{
		"ctid":          float64(133),
		"ip":            "192.168.10.90",
		"hostname":      "archivebox",
		"has_relay_ssh": true,
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var c containers.Container
	if err := json.Unmarshal([]byte(result.Content[0].Text), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.CTID != 133 || c.IP != "192.168.10.90" || !c.HasRelaySSH {
		t.Fatalf("unexpected container: %+v", c)
	}
}

func TestRegisterContainerMissingFields(t *testing.T) {
	h := setup(t)

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing ctid", map[string]interface{}{"ip": "1.2.3.4", "hostname": "test"}},
		{"missing ip", map[string]interface{}{"ctid": float64(100), "hostname": "test"}},
		{"missing hostname", map[string]interface{}{"ctid": float64(100), "ip": "1.2.3.4"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := h.Handle("register_container", tt.args)
			if !result.IsError {
				t.Fatal("expected error for missing field")
			}
		})
	}
}

func TestListContainersEmpty(t *testing.T) {
	h := setup(t)

	result := h.Handle("list_containers", map[string]interface{}{})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var list []containers.Container
	if err := json.Unmarshal([]byte(result.Content[0].Text), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d", len(list))
	}
}

func TestListContainersAfterRegister(t *testing.T) {
	h := setup(t)

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(100), "ip": "192.168.10.52", "hostname": "ingress",
	})
	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox",
	})

	result := h.Handle("list_containers", map[string]interface{}{})
	var list []containers.Container
	json.Unmarshal([]byte(result.Content[0].Text), &list)

	if len(list) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(list))
	}
}

func TestExecContainerNotFound(t *testing.T) {
	h := setup(t)

	result := h.Handle("exec_container", map[string]interface{}{
		"ctid":    float64(999),
		"command": "hostname",
		"reason":  "test",
	})

	if !result.IsError {
		t.Fatal("expected error for unregistered container")
	}
	if result.Content[0].Text == "" {
		t.Fatal("expected error message")
	}
}

func TestExecContainerDirectSSH(t *testing.T) {
	h := setup(t)

	// Register with direct SSH
	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": true,
	})

	result := h.Handle("exec_container", map[string]interface{}{
		"ctid":    float64(133),
		"command": "docker",
		"args":    []interface{}{"compose", "ps"},
		"reason":  "Check services",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	if resp["route"] != "direct_ssh" {
		t.Fatalf("expected direct_ssh route, got %s", resp["route"])
	}
	if resp["request_id"] == "" {
		t.Fatal("expected request_id")
	}

	// Verify the store request was created with the right SSH command
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)
	if req == nil {
		t.Fatal("request not found in store")
	}
	if req.Command != "ssh" {
		t.Fatalf("expected ssh command, got %s", req.Command)
	}
	// Should be: ssh root@192.168.10.90 -- docker compose ps
	expected := []string{"root@192.168.10.90", "--", "docker", "compose", "ps"}
	if len(req.Args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(req.Args), req.Args)
	}
	for i, a := range expected {
		if req.Args[i] != a {
			t.Fatalf("arg[%d]: expected %q, got %q", i, a, req.Args[i])
		}
	}
}

func TestExecContainerPctExecFallback(t *testing.T) {
	h := setup(t)

	// Register without direct SSH
	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": false,
	})

	result := h.Handle("exec_container", map[string]interface{}{
		"ctid":    float64(133),
		"command": "hostname",
		"reason":  "test",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	if resp["route"] != "pct_exec" {
		t.Fatalf("expected pct_exec route, got %s", resp["route"])
	}

	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)
	// Should be: ssh root@192.168.10.50 pct exec 133 -- hostname
	expected := []string{"root@192.168.10.50", "pct", "exec", "133", "--", "hostname"}
	if len(req.Args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(req.Args), req.Args)
	}
	for i, a := range expected {
		if req.Args[i] != a {
			t.Fatalf("arg[%d]: expected %q, got %q", i, a, req.Args[i])
		}
	}
}

func TestExecContainerShellMode(t *testing.T) {
	h := setup(t)

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": true,
	})

	result := h.Handle("exec_container", map[string]interface{}{
		"ctid":    float64(133),
		"command": "cat /etc/hostname | head -1",
		"reason":  "test shell mode",
		"shell":   true,
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)
	// Should be: ssh root@192.168.10.90 -- "cat /etc/hostname | head -1"
	// (no sh -c: SSH passes args to the remote shell directly)
	expected := []string{"root@192.168.10.90", "--", "cat /etc/hostname | head -1"}
	if len(req.Args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(req.Args), req.Args)
	}
	for i, a := range expected {
		if req.Args[i] != a {
			t.Fatalf("arg[%d]: expected %q, got %q", i, a, req.Args[i])
		}
	}
}

func TestExecContainerReasonPrefix(t *testing.T) {
	h := setup(t)

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": true,
	})

	result := h.Handle("exec_container", map[string]interface{}{
		"ctid":    float64(133),
		"command": "hostname",
		"reason":  "Check identity",
	})

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)
	if req.Reason != "[CTID 133 archivebox] Check identity" {
		t.Fatalf("expected prefixed reason, got %q", req.Reason)
	}
}

func TestExecContainerMissingFields(t *testing.T) {
	h := setup(t)

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing ctid", map[string]interface{}{"command": "ls", "reason": "test"}},
		{"missing command", map[string]interface{}{"ctid": float64(133), "reason": "test"}},
		{"missing reason", map[string]interface{}{"ctid": float64(133), "command": "ls"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := h.Handle("exec_container", tt.args)
			if !result.IsError {
				t.Fatal("expected error for missing field")
			}
		})
	}
}

func TestExistingToolsUnchanged(t *testing.T) {
	h := setup(t)

	// request_command still works
	result := h.Handle("request_command", map[string]interface{}{
		"command": "echo",
		"reason":  "test",
	})
	if result.IsError {
		t.Fatalf("request_command failed: %s", result.Content[0].Text)
	}

	// Parse the request ID from the JSON response
	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)
	reqID := resp["request_id"].(string)

	// get_result still works
	result = h.Handle("get_result", map[string]interface{}{
		"request_id": reqID,
	})
	if result.IsError {
		t.Fatalf("get_result failed: %s", result.Content[0].Text)
	}

	// list_requests still works
	result = h.Handle("list_requests", map[string]interface{}{})
	if result.IsError {
		t.Fatalf("list_requests failed: %s", result.Content[0].Text)
	}
}
