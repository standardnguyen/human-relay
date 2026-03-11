package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Container matches the JSON returned by register_container / list_containers.
type Container struct {
	CTID        int    `json:"ctid"`
	IP          string `json:"ip"`
	Hostname    string `json:"hostname"`
	HasRelaySSH bool   `json:"has_relay_ssh"`
	SSHUser     string `json:"ssh_user,omitempty"`
}

// ExecResponse matches the JSON returned by exec_container.
type ExecResponse struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Container string `json:"container"`
	Route     string `json:"route"`
}

// initClient creates a server + MCP client with initialize handshake done.
func initClient(t *testing.T, opts ...ServerOption) (*TestServer, *MCPClient) {
	t.Helper()
	s := StartServer(t, opts...)
	c := NewMCPClient(t, s.MCPURL())
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)
	return s, c
}

// extractContainer parses a single Container from an MCP tools/call response.
func extractContainer(t *testing.T, resp *JSONRPCResponse) Container {
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
	var c Container
	if err := json.Unmarshal([]byte(result.Content[0].Text), &c); err != nil {
		t.Fatalf("failed to parse container: %v\nraw: %s", err, result.Content[0].Text)
	}
	return c
}

// extractContainerList parses a []Container from an MCP tools/call response.
func extractContainerList(t *testing.T, resp *JSONRPCResponse) []Container {
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
	var list []Container
	if err := json.Unmarshal([]byte(result.Content[0].Text), &list); err != nil {
		t.Fatalf("failed to parse container list: %v\nraw: %s", err, result.Content[0].Text)
	}
	return list
}

// extractExecResponse parses an ExecResponse from an MCP tools/call response.
func extractExecResponse(t *testing.T, resp *JSONRPCResponse) ExecResponse {
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
	var er ExecResponse
	if err := json.Unmarshal([]byte(result.Content[0].Text), &er); err != nil {
		t.Fatalf("failed to parse exec response: %v\nraw: %s", err, result.Content[0].Text)
	}
	return er
}

// isErrorResponse returns true if the MCP response indicates an error
// (either RPC-level or tool-level).
func isErrorResponse(resp *JSONRPCResponse) bool {
	if resp.Error != nil {
		return true
	}
	var result struct {
		IsError bool `json:"isError"`
	}
	json.Unmarshal(resp.Result, &result)
	return result.IsError
}

// findRequestByID calls list_requests and returns the request matching the given ID.
func findRequestByID(t *testing.T, c *MCPClient, callID int, requestID string) *RequestResult {
	t.Helper()
	listResp := c.Call(t, callID, "tools/call", map[string]interface{}{
		"name": "list_requests", "arguments": map[string]interface{}{},
	})
	requests := extractList(t, listResp)
	for i := range requests {
		if requests[i].ID == requestID {
			return &requests[i]
		}
	}
	t.Fatalf("request %s not found in list_requests (%d requests returned)", requestID, len(requests))
	return nil
}

// registerContainer registers a container via MCP and fatals on error.
func registerContainer(t *testing.T, c *MCPClient, callID int, ctid float64, ip, hostname string, hasRelaySSH bool) {
	t.Helper()
	resp := c.Call(t, callID, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid": ctid, "ip": ip, "hostname": hostname, "has_relay_ssh": hasRelaySSH,
		},
	})
	if isErrorResponse(resp) {
		t.Fatalf("register_container(%v, %s, %s) failed", ctid, ip, hostname)
	}
}

// assertArgs checks that got matches want element-by-element, with clear diffs.
func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args length: expected %d, got %d\n  want: %v\n  got:  %v", len(want), len(got), want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d]: expected %q, got %q", i, want[i], got[i])
		}
	}
}

// --- register_container tests ---

func TestRegisterContainer(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          float64(133),
			"ip":            "192.168.10.90",
			"hostname":      "archivebox",
			"has_relay_ssh": true,
		},
	})

	if isErrorResponse(resp) {
		t.Fatalf("unexpected error")
	}

	ct := extractContainer(t, resp)
	if ct.CTID != 133 {
		t.Errorf("expected CTID 133, got %d", ct.CTID)
	}
	if ct.IP != "192.168.10.90" {
		t.Errorf("expected IP 192.168.10.90, got %s", ct.IP)
	}
	if ct.Hostname != "archivebox" {
		t.Errorf("expected hostname archivebox, got %s", ct.Hostname)
	}
	if !ct.HasRelaySSH {
		t.Error("expected has_relay_ssh to be true")
	}
}

func TestRegisterContainerMissingFields(t *testing.T) {
	_, c := initClient(t)

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing ctid", map[string]interface{}{"ip": "1.2.3.4", "hostname": "test"}},
		{"missing ip", map[string]interface{}{"ctid": float64(100), "hostname": "test"}},
		{"missing hostname", map[string]interface{}{"ctid": float64(100), "ip": "1.2.3.4"}},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := c.Call(t, i+2, "tools/call", map[string]interface{}{
				"name":      "register_container",
				"arguments": tt.args,
			})
			if !isErrorResponse(resp) {
				t.Fatal("expected error for missing field")
			}
		})
	}
}

func TestRegisterContainerUpsert(t *testing.T) {
	_, c := initClient(t)

	// Register initial
	c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox",
		},
	})

	// Update same CTID
	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          float64(133),
			"ip":            "192.168.10.91",
			"hostname":      "archivebox-v2",
			"has_relay_ssh": true,
		},
	})

	ct := extractContainer(t, resp)
	if ct.IP != "192.168.10.91" {
		t.Errorf("expected updated IP 192.168.10.91, got %s", ct.IP)
	}
	if ct.Hostname != "archivebox-v2" {
		t.Errorf("expected updated hostname archivebox-v2, got %s", ct.Hostname)
	}
	if !ct.HasRelaySSH {
		t.Error("expected has_relay_ssh to be true after upsert")
	}

	// List should show only 1 container
	listResp := c.Call(t, 4, "tools/call", map[string]interface{}{
		"name": "list_containers", "arguments": map[string]interface{}{},
	})
	list := extractContainerList(t, listResp)
	if len(list) != 1 {
		t.Errorf("expected 1 container after upsert, got %d", len(list))
	}
}

// --- list_containers tests ---

func TestListContainersEmpty(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "list_containers", "arguments": map[string]interface{}{},
	})

	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	list := extractContainerList(t, resp)
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d containers", len(list))
	}
}

func TestListContainersOrdered(t *testing.T) {
	_, c := initClient(t)

	// Register in non-sorted order
	for i, ct := range []struct {
		ctid     float64
		ip       string
		hostname string
	}{
		{133, "192.168.10.90", "archivebox"},
		{100, "192.168.10.52", "ingress"},
		{115, "192.168.10.66", "claude-code"},
	} {
		c.Call(t, i+2, "tools/call", map[string]interface{}{
			"name": "register_container",
			"arguments": map[string]interface{}{
				"ctid": ct.ctid, "ip": ct.ip, "hostname": ct.hostname,
			},
		})
	}

	resp := c.Call(t, 5, "tools/call", map[string]interface{}{
		"name": "list_containers", "arguments": map[string]interface{}{},
	})

	list := extractContainerList(t, resp)
	if len(list) != 3 {
		t.Fatalf("expected 3 containers, got %d", len(list))
	}
	// Should be ordered by CTID
	if list[0].CTID != 100 || list[1].CTID != 115 || list[2].CTID != 133 {
		t.Errorf("wrong order: %d, %d, %d", list[0].CTID, list[1].CTID, list[2].CTID)
	}
}

// --- exec_container tests ---

func TestExecContainerNotFound(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(999),
			"command": "hostname",
			"reason":  "test",
		},
	})

	if !isErrorResponse(resp) {
		t.Fatal("expected error for unregistered container")
	}
}

func TestExecContainerDirectSSH(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 133, "192.168.10.90", "archivebox", true)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(133),
			"command": "docker",
			"args":    []interface{}{"compose", "ps"},
			"reason":  "Check services",
		},
	})

	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	er := extractExecResponse(t, resp)
	if er.Route != "direct_ssh" {
		t.Errorf("expected route direct_ssh, got %s", er.Route)
	}
	if er.RequestID == "" {
		t.Fatal("expected non-empty request_id")
	}
	if er.Status != "pending" {
		t.Errorf("expected status pending, got %s", er.Status)
	}

	// Verify the underlying request has the right SSH command structure
	found := findRequestByID(t, c, 4, er.RequestID)

	if found.Command != "ssh" {
		t.Errorf("expected command ssh, got %s", found.Command)
	}
	// Expected: ssh root@192.168.10.90 -- docker compose ps
	assertArgs(t, found.Args, []string{"root@192.168.10.90", "--", "docker", "compose", "ps"})

	// Deny so it doesn't try to actually SSH
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), er.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestExecContainerPctExecFallback(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 133, "192.168.10.90", "archivebox", false)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(133),
			"command": "hostname",
			"reason":  "test",
		},
	})

	er := extractExecResponse(t, resp)
	if er.Route != "pct_exec" {
		t.Errorf("expected route pct_exec, got %s", er.Route)
	}

	// Verify SSH args: ssh root@192.168.10.50 pct exec 133 -- hostname
	found := findRequestByID(t, c, 4, er.RequestID)
	assertArgs(t, found.Args, []string{"root@192.168.10.50", "pct", "exec", "133", "--", "hostname"})

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), er.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestExecContainerShellMode(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 133, "192.168.10.90", "archivebox", true)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(133),
			"command": "cat /etc/hostname | head -1",
			"reason":  "test shell mode",
			"shell":   true,
		},
	})

	er := extractExecResponse(t, resp)

	// Verify SSH args: ssh root@192.168.10.90 -- "cat /etc/hostname | head -1"
	// (no sh -c: SSH passes args to the remote shell directly)
	found := findRequestByID(t, c, 4, er.RequestID)
	assertArgs(t, found.Args, []string{"root@192.168.10.90", "--", "cat /etc/hostname | head -1"})

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), er.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestExecContainerReasonPrefix(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 133, "192.168.10.90", "archivebox", true)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(133),
			"command": "hostname",
			"reason":  "Check identity",
		},
	})

	er := extractExecResponse(t, resp)
	found := findRequestByID(t, c, 4, er.RequestID)

	expected := "[CTID 133 archivebox] Check identity"
	if found.Reason != expected {
		t.Errorf("expected reason %q, got %q", expected, found.Reason)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), er.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestExecContainerMissingFields(t *testing.T) {
	_, c := initClient(t)

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing ctid", map[string]interface{}{"command": "ls", "reason": "test"}},
		{"missing command", map[string]interface{}{"ctid": float64(133), "reason": "test"}},
		{"missing reason", map[string]interface{}{"ctid": float64(133), "command": "ls"}},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := c.Call(t, i+2, "tools/call", map[string]interface{}{
				"name":      "exec_container",
				"arguments": tt.args,
			})
			if !isErrorResponse(resp) {
				t.Fatal("expected error for missing field")
			}
		})
	}
}

func TestExecContainerPctExecShellMode(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 133, "192.168.10.90", "archivebox", false)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(133),
			"command": "ls -la /opt",
			"reason":  "test pct exec shell",
			"shell":   true,
		},
	})

	er := extractExecResponse(t, resp)
	if er.Route != "pct_exec" {
		t.Errorf("expected route pct_exec, got %s", er.Route)
	}

	// Verify: ssh root@192.168.10.50 pct exec 133 -- sh -c 'ls -la /opt'
	found := findRequestByID(t, c, 4, er.RequestID)
	assertArgs(t, found.Args, []string{"root@192.168.10.50", "pct", "exec", "133", "--", "sh", "-c", "'ls -la /opt'"})

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), er.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestExecContainerResponseFields(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, c, 2, 133, "192.168.10.90", "archivebox", true)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(133),
			"command": "hostname",
			"reason":  "test",
		},
	})

	er := extractExecResponse(t, resp)
	if er.RequestID == "" {
		t.Error("expected non-empty request_id")
	}
	if er.Status != "pending" {
		t.Errorf("expected status pending, got %s", er.Status)
	}
	if !strings.Contains(er.Container, "133") || !strings.Contains(er.Container, "archivebox") {
		t.Errorf("expected container field to contain CTID and hostname, got %q", er.Container)
	}
	if er.Route != "direct_ssh" {
		t.Errorf("expected route direct_ssh, got %s", er.Route)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), er.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestContainerRegistryPersistence(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "hr-persist-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataDir)

	// Start server 1, register a container, then kill it
	s1 := StartServer(t, WithDataDir(dataDir), WithPorts(18180+os.Getpid()%1000, 19190+os.Getpid()%1000))
	c1 := NewMCPClient(t, s1.MCPURL())
	c1.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c1.Notify(t, "notifications/initialized", nil)

	registerContainer(t, c1, 2, 133, "192.168.10.90", "archivebox", true)

	// Verify it's there
	listResp := c1.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "list_containers", "arguments": map[string]interface{}{},
	})
	list := extractContainerList(t, listResp)
	if len(list) != 1 || list[0].CTID != 133 {
		t.Fatalf("expected 1 container (CTID 133), got %d", len(list))
	}

	// Kill server 1
	s1.cmd.Process.Kill()
	s1.cmd.Wait()

	// Start server 2 with the same data dir but different ports
	s2 := StartServer(t, WithDataDir(dataDir), WithPorts(18280+os.Getpid()%1000, 19290+os.Getpid()%1000))
	c2 := NewMCPClient(t, s2.MCPURL())
	c2.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c2.Notify(t, "notifications/initialized", nil)

	// Container should still be there
	listResp2 := c2.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "list_containers", "arguments": map[string]interface{}{},
	})
	list2 := extractContainerList(t, listResp2)
	if len(list2) != 1 {
		t.Fatalf("expected 1 container after restart, got %d", len(list2))
	}
	if list2[0].CTID != 133 {
		t.Errorf("expected CTID 133, got %d", list2[0].CTID)
	}
	if list2[0].IP != "192.168.10.90" {
		t.Errorf("expected IP 192.168.10.90, got %s", list2[0].IP)
	}
	if list2[0].Hostname != "archivebox" {
		t.Errorf("expected hostname archivebox, got %s", list2[0].Hostname)
	}
	if !list2[0].HasRelaySSH {
		t.Error("expected has_relay_ssh to be true after restart")
	}
}

// --- ssh_user tests ---

func TestRegisterContainerWithSSHUser(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          float64(9999),
			"ip":            "192.168.10.104",
			"hostname":      "corsair",
			"has_relay_ssh": true,
			"ssh_user":      "Lara Duong",
		},
	})

	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	ct := extractContainer(t, resp)
	if ct.SSHUser != "Lara Duong" {
		t.Errorf("expected ssh_user 'Lara Duong', got %q", ct.SSHUser)
	}
}

func TestExecContainerCustomSSHUser(t *testing.T) {
	s, c := initClient(t)

	// Register with custom ssh_user
	c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          float64(9999),
			"ip":            "192.168.10.104",
			"hostname":      "corsair",
			"has_relay_ssh": true,
			"ssh_user":      "Lara Duong",
		},
	})

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(9999),
			"command": "whoami",
			"reason":  "test custom ssh user",
		},
	})

	if isErrorResponse(resp) {
		t.Fatal("unexpected error")
	}

	er := extractExecResponse(t, resp)
	found := findRequestByID(t, c, 4, er.RequestID)

	// Should use "Lara Duong@192.168.10.104" not "root@192.168.10.104"
	assertArgs(t, found.Args, []string{"Lara Duong@192.168.10.104", "--", "whoami"})

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), er.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestExecContainerDefaultSSHUser(t *testing.T) {
	s, c := initClient(t)

	// Register without ssh_user — should default to root
	registerContainer(t, c, 2, 133, "192.168.10.90", "archivebox", true)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    float64(133),
			"command": "whoami",
			"reason":  "test default ssh user",
		},
	})

	er := extractExecResponse(t, resp)
	found := findRequestByID(t, c, 4, er.RequestID)

	// Should still use root@ when no ssh_user set
	assertArgs(t, found.Args, []string{"root@192.168.10.90", "--", "whoami"})

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), er.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

func TestSSHUserPreservedOnUpsert(t *testing.T) {
	_, c := initClient(t)

	// Register with ssh_user
	c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          float64(9999),
			"ip":            "192.168.10.104",
			"hostname":      "corsair",
			"has_relay_ssh": true,
			"ssh_user":      "Lara Duong",
		},
	})

	// Upsert without ssh_user — should preserve existing value
	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          float64(9999),
			"ip":            "192.168.10.105",
			"hostname":      "corsair-v2",
			"has_relay_ssh": true,
		},
	})

	ct := extractContainer(t, resp)
	if ct.IP != "192.168.10.105" {
		t.Errorf("expected updated IP, got %s", ct.IP)
	}
	if ct.SSHUser != "Lara Duong" {
		t.Errorf("expected ssh_user preserved as 'Lara Duong', got %q", ct.SSHUser)
	}
}

func TestSSHUserPersistence(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "hr-sshuser-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dataDir)

	// Start server 1, register with ssh_user
	s1 := StartServer(t, WithDataDir(dataDir), WithPorts(18380+os.Getpid()%1000, 19390+os.Getpid()%1000))
	c1 := NewMCPClient(t, s1.MCPURL())
	c1.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c1.Notify(t, "notifications/initialized", nil)

	c1.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          float64(9999),
			"ip":            "192.168.10.104",
			"hostname":      "corsair",
			"has_relay_ssh": true,
			"ssh_user":      "Lara Duong",
		},
	})

	s1.cmd.Process.Kill()
	s1.cmd.Wait()

	// Start server 2 with same data dir
	s2 := StartServer(t, WithDataDir(dataDir), WithPorts(18480+os.Getpid()%1000, 19490+os.Getpid()%1000))
	c2 := NewMCPClient(t, s2.MCPURL())
	c2.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c2.Notify(t, "notifications/initialized", nil)

	listResp := c2.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "list_containers", "arguments": map[string]interface{}{},
	})
	list := extractContainerList(t, listResp)
	if len(list) != 1 {
		t.Fatalf("expected 1 container after restart, got %d", len(list))
	}
	if list[0].SSHUser != "Lara Duong" {
		t.Errorf("expected ssh_user 'Lara Duong' after restart, got %q", list[0].SSHUser)
	}
}
