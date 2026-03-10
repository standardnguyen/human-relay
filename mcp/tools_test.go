package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// --- install_relay_ssh tests ---

func writeTempPubkey(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "id_ed25519.pub")
	os.WriteFile(path, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICW5uXN0QdRfh/n37dTK3nbZd1PxxVP/iFodW7QrJDYk human-relay\n"), 0644)
	return path
}

func TestInstallRelaySSHBasic(t *testing.T) {
	h := setup(t)
	h.SetRelayPubkeyFile(writeTempPubkey(t))

	// Register the container first (without relay SSH)
	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(125), "ip": "192.168.10.102", "hostname": "recovery-check", "has_relay_ssh": false,
	})

	result := h.Handle("install_relay_ssh", map[string]interface{}{
		"ctid":   float64(125),
		"reason": "Enable direct SSH for future commands",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	if resp["route"] != "pct_push" {
		t.Fatalf("expected pct_push route, got %s", resp["route"])
	}
	if resp["request_id"] == "" {
		t.Fatal("expected request_id")
	}

	// Verify the store request was created with pct push through host
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)
	if req == nil {
		t.Fatal("request not found in store")
	}
	if req.Command != "ssh" {
		t.Fatalf("expected ssh command, got %s", req.Command)
	}
	// Should route through host: ssh root@192.168.10.50 -- sh -c "..."
	if req.Args[0] != "root@192.168.10.50" {
		t.Fatalf("expected root@host arg, got %q", req.Args[0])
	}
	if req.Args[1] != "--" {
		t.Fatalf("expected -- separator, got %q", req.Args[1])
	}
	if req.Args[2] != "sh" {
		t.Fatalf("expected sh, got %q", req.Args[2])
	}
	if req.Args[3] != "-c" {
		t.Fatalf("expected -c, got %q", req.Args[3])
	}
	// The shell command should contain pct push and pct exec for CTID 125
	shellCmd := req.Args[4]
	if !strings.Contains(shellCmd, "pct push 125") {
		t.Fatalf("shell command should contain 'pct push 125', got: %s", shellCmd)
	}
	if !strings.Contains(shellCmd, "pct exec 125") {
		t.Fatalf("shell command should contain 'pct exec 125', got: %s", shellCmd)
	}
	if !strings.Contains(shellCmd, "authorized_keys") {
		t.Fatalf("shell command should reference authorized_keys, got: %s", shellCmd)
	}
	if !strings.Contains(shellCmd, "mkdir -p /root/.ssh") {
		t.Fatalf("shell command should mkdir .ssh, got: %s", shellCmd)
	}

	// Verify stdin contains the public key
	if req.Stdin == nil {
		t.Fatal("expected stdin with public key content")
	}
	stdinStr := string(req.Stdin)
	if !strings.Contains(stdinStr, "ssh-ed25519") {
		t.Fatalf("stdin should contain the public key, got: %s", stdinStr)
	}
}

func TestInstallRelaySSHAlwaysRoutesThroughHost(t *testing.T) {
	h := setup(t)
	h.SetRelayPubkeyFile(writeTempPubkey(t))

	// Register WITH relay SSH already — install_relay_ssh should still use pct_push
	// (the whole point is bootstrapping, so we always go through host)
	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(125), "ip": "192.168.10.102", "hostname": "recovery-check", "has_relay_ssh": true,
	})

	result := h.Handle("install_relay_ssh", map[string]interface{}{
		"ctid":   float64(125),
		"reason": "Re-install relay key",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	if resp["route"] != "pct_push" {
		t.Fatalf("install_relay_ssh should always use pct_push, got %s", resp["route"])
	}
}

func TestInstallRelaySSHUnregisteredContainer(t *testing.T) {
	h := setup(t)
	h.SetRelayPubkeyFile(writeTempPubkey(t))

	// Don't register anything — should still work (unregistered container)
	result := h.Handle("install_relay_ssh", map[string]interface{}{
		"ctid":   float64(125),
		"reason": "Bootstrap new container",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	if resp["route"] != "pct_push" {
		t.Fatalf("expected pct_push, got %s", resp["route"])
	}
}

func TestInstallRelaySSHNoPubkeyConfigured(t *testing.T) {
	h := setup(t)
	// Don't set relay pubkey file

	result := h.Handle("install_relay_ssh", map[string]interface{}{
		"ctid":   float64(125),
		"reason": "test",
	})

	if !result.IsError {
		t.Fatal("expected error when relay pubkey file not configured")
	}
	if !strings.Contains(result.Content[0].Text, "not configured") {
		t.Fatalf("expected 'not configured' error, got: %s", result.Content[0].Text)
	}
}

func TestInstallRelaySSHBadPubkeyFile(t *testing.T) {
	h := setup(t)
	h.SetRelayPubkeyFile("/nonexistent/path/id_ed25519.pub")

	result := h.Handle("install_relay_ssh", map[string]interface{}{
		"ctid":   float64(125),
		"reason": "test",
	})

	if !result.IsError {
		t.Fatal("expected error when pubkey file doesn't exist")
	}
	if !strings.Contains(result.Content[0].Text, "failed to read") {
		t.Fatalf("expected 'failed to read' error, got: %s", result.Content[0].Text)
	}
}

func TestInstallRelaySSHInvalidPubkeyContent(t *testing.T) {
	h := setup(t)
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.pub")
	os.WriteFile(badPath, []byte("this is not a valid ssh key"), 0644)
	h.SetRelayPubkeyFile(badPath)

	result := h.Handle("install_relay_ssh", map[string]interface{}{
		"ctid":   float64(125),
		"reason": "test",
	})

	if !result.IsError {
		t.Fatal("expected error for invalid pubkey content")
	}
	if !strings.Contains(result.Content[0].Text, "valid SSH public key") {
		t.Fatalf("expected validation error, got: %s", result.Content[0].Text)
	}
}

func TestInstallRelaySSHMissingFields(t *testing.T) {
	h := setup(t)
	h.SetRelayPubkeyFile(writeTempPubkey(t))

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing ctid", map[string]interface{}{"reason": "test"}},
		{"missing reason", map[string]interface{}{"ctid": float64(125)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := h.Handle("install_relay_ssh", tt.args)
			if !result.IsError {
				t.Fatal("expected error for missing field")
			}
		})
	}
}

func TestInstallRelaySSHDisplayCommand(t *testing.T) {
	h := setup(t)
	h.SetRelayPubkeyFile(writeTempPubkey(t))

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(125), "ip": "192.168.10.102", "hostname": "recovery-check",
	})

	result := h.Handle("install_relay_ssh", map[string]interface{}{
		"ctid":   float64(125),
		"reason": "test display",
	})

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)

	if !strings.Contains(req.DisplayCommand, "relay SSH key") {
		t.Fatalf("display command should mention relay SSH key, got: %s", req.DisplayCommand)
	}
	if !strings.Contains(req.DisplayCommand, "125") {
		t.Fatalf("display command should mention CTID, got: %s", req.DisplayCommand)
	}
}

func TestInstallRelaySSHWithSSHConfig(t *testing.T) {
	h := setup(t)
	h.SetRelayPubkeyFile(writeTempPubkey(t))
	h.SetSSHConfig("/etc/ssh/custom_config")

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(125), "ip": "192.168.10.102", "hostname": "recovery-check",
	})

	result := h.Handle("install_relay_ssh", map[string]interface{}{
		"ctid":   float64(125),
		"reason": "test ssh config",
	})

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)

	// First two args should be -F /etc/ssh/custom_config
	if req.Args[0] != "-F" || req.Args[1] != "/etc/ssh/custom_config" {
		t.Fatalf("expected SSH config prefix, got args: %v", req.Args)
	}
}

// --- install_ssh_key tests ---

func TestInstallSSHKeyDirectSSH(t *testing.T) {
	h := setup(t)

	// Register with relay SSH access
	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": true,
	})

	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyData12345678901234567890123456 user@host"
	result := h.Handle("install_ssh_key", map[string]interface{}{
		"ctid":       float64(133),
		"public_key": key,
		"reason":     "Grant access to claude-115",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	if resp["route"] != "direct_ssh" {
		t.Fatalf("expected direct_ssh route, got %s", resp["route"])
	}

	// Verify warning is present
	if resp["warning"] == nil || resp["warning"] == "" {
		t.Fatal("expected warning about arbitrary SSH key")
	}

	// Verify store request
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)

	// Should SSH directly to container: ssh root@192.168.10.90 -- sh -c "..."
	if req.Args[0] != "root@192.168.10.90" {
		t.Fatalf("expected direct SSH to container, got %q", req.Args[0])
	}
	// Args: [root@IP, --, sh, -c, <shell command>]
	if len(req.Args) < 5 {
		t.Fatalf("expected at least 5 args, got %d: %v", len(req.Args), req.Args)
	}
	shellCmd := req.Args[4]
	if !strings.Contains(shellCmd, "authorized_keys") {
		t.Fatalf("shell command should reference authorized_keys, got: %s", shellCmd)
	}

	// Verify stdin contains the key
	stdinStr := string(req.Stdin)
	if !strings.Contains(stdinStr, "ssh-ed25519") {
		t.Fatalf("stdin should contain the public key, got: %s", stdinStr)
	}
}

func TestInstallSSHKeyPctPushFallback(t *testing.T) {
	h := setup(t)

	// Register WITHOUT relay SSH access
	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": false,
	})

	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyData12345678901234567890123456 user@host"
	result := h.Handle("install_ssh_key", map[string]interface{}{
		"ctid":       float64(133),
		"public_key": key,
		"reason":     "Grant access",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)

	if resp["route"] != "pct_push" {
		t.Fatalf("expected pct_push route, got %s", resp["route"])
	}

	// Verify store request routes through host
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)

	if req.Args[0] != "root@192.168.10.50" {
		t.Fatalf("expected route through host, got %q", req.Args[0])
	}
	shellCmd := req.Args[4] // root@host -- sh -c <cmd>
	if !strings.Contains(shellCmd, "pct push 133") {
		t.Fatalf("shell command should contain 'pct push 133', got: %s", shellCmd)
	}
	if !strings.Contains(shellCmd, "pct exec 133") {
		t.Fatalf("shell command should contain 'pct exec 133', got: %s", shellCmd)
	}
}

func TestInstallSSHKeyContainerNotFound(t *testing.T) {
	h := setup(t)

	result := h.Handle("install_ssh_key", map[string]interface{}{
		"ctid":       float64(999),
		"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyData12345678901234567890123456 user@host",
		"reason":     "test",
	})

	if !result.IsError {
		t.Fatal("expected error for unregistered container")
	}
	if !strings.Contains(result.Content[0].Text, "not found") {
		t.Fatalf("expected 'not found' error, got: %s", result.Content[0].Text)
	}
}

func TestInstallSSHKeyInvalidKey(t *testing.T) {
	h := setup(t)

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox",
	})

	tests := []struct {
		name string
		key  string
	}{
		{"empty key", ""},
		{"random text", "not-a-real-key"},
		{"missing algo", "AAAAC3NzaC1lZDI1NTE5AAAAITest user@host"},
		{"private key marker", "-----BEGIN OPENSSH PRIVATE KEY-----"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := h.Handle("install_ssh_key", map[string]interface{}{
				"ctid":       float64(133),
				"public_key": tt.key,
				"reason":     "test",
			})
			if !result.IsError {
				t.Fatalf("expected error for invalid key %q", tt.key)
			}
		})
	}
}

func TestInstallSSHKeyMissingFields(t *testing.T) {
	h := setup(t)

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox",
	})

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing ctid", map[string]interface{}{"public_key": "ssh-ed25519 AAAA test", "reason": "test"}},
		{"missing public_key", map[string]interface{}{"ctid": float64(133), "reason": "test"}},
		{"missing reason", map[string]interface{}{"ctid": float64(133), "public_key": "ssh-ed25519 AAAA test"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := h.Handle("install_ssh_key", tt.args)
			if !result.IsError {
				t.Fatal("expected error for missing field")
			}
		})
	}
}

func TestInstallSSHKeyReasonPrefix(t *testing.T) {
	h := setup(t)

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": true,
	})

	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyData12345678901234567890123456 user@host"
	result := h.Handle("install_ssh_key", map[string]interface{}{
		"ctid":       float64(133),
		"public_key": key,
		"reason":     "Grant access to claude-115",
	})

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)

	if !strings.Contains(req.Reason, "[SSH KEY -> CTID 133 archivebox]") {
		t.Fatalf("reason should be prefixed with container context, got: %s", req.Reason)
	}
	if !strings.Contains(req.Reason, key) {
		t.Fatalf("reason should contain the key for reviewer inspection, got: %s", req.Reason)
	}
}

func TestInstallSSHKeyDisplayCommand(t *testing.T) {
	h := setup(t)

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": true,
	})

	result := h.Handle("install_ssh_key", map[string]interface{}{
		"ctid":       float64(133),
		"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyData12345678901234567890123456 user@host",
		"reason":     "test",
	})

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)

	if !strings.Contains(req.DisplayCommand, "install SSH key") {
		t.Fatalf("display command should mention SSH key install, got: %s", req.DisplayCommand)
	}
}

func TestInstallSSHKeyWithSSHConfig(t *testing.T) {
	h := setup(t)
	h.SetSSHConfig("/etc/ssh/custom_config")

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": true,
	})

	result := h.Handle("install_ssh_key", map[string]interface{}{
		"ctid":       float64(133),
		"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyData12345678901234567890123456 user@host",
		"reason":     "test",
	})

	var resp map[string]interface{}
	json.Unmarshal([]byte(result.Content[0].Text), &resp)
	reqID := resp["request_id"].(string)
	req := h.store.Get(reqID)

	if req.Args[0] != "-F" || req.Args[1] != "/etc/ssh/custom_config" {
		t.Fatalf("expected SSH config prefix, got args: %v", req.Args)
	}
}

func TestInstallSSHKeyRSAKey(t *testing.T) {
	h := setup(t)

	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox", "has_relay_ssh": true,
	})

	// RSA key format should also be accepted
	key := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC1234567890abcdef user@host"
	result := h.Handle("install_ssh_key", map[string]interface{}{
		"ctid":       float64(133),
		"public_key": key,
		"reason":     "test RSA key",
	})

	if result.IsError {
		t.Fatalf("RSA key should be accepted: %s", result.Content[0].Text)
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
