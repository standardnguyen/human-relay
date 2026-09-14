package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Container matches the JSON returned by list_containers (and by an approved
// register_container request's stdout).
type Container struct {
	CTID        int    `json:"ctid"`
	IP          string `json:"ip"`
	Hostname    string `json:"hostname"`
	HasRelaySSH bool   `json:"has_relay_ssh"`
	SSHUser     string `json:"ssh_user,omitempty"`
}

// Machine matches the JSON returned by list_machines.
type Machine struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	SSHUser      string `json:"ssh_user"`
	Shell        string `json:"shell"`
	IdentityFile string `json:"identity_file,omitempty"`
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

// toolText returns the first text block of an MCP tools/call response.
func toolText(t *testing.T, resp *JSONRPCResponse) string {
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
	return result.Content[0].Text
}

// extractPendingID asserts that a registry tool queued a request for human
// approval — {"request_id":..., "status":"pending"} — and returns the ID.
// Registry mutations stopped being synchronous with finding #23 part 2.
func extractPendingID(t *testing.T, resp *JSONRPCResponse) string {
	t.Helper()
	if isErrorResponse(resp) {
		t.Fatalf("tool returned an error: %s", toolText(t, resp))
	}
	text := toolText(t, resp)
	var out struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("failed to parse pending envelope: %v\nraw: %s", err, text)
	}
	if out.Status != "pending" {
		t.Fatalf("expected status pending, got %q\nraw: %s", out.Status, text)
	}
	if out.RequestID == "" {
		t.Fatalf("empty request_id\nraw: %s", text)
	}
	return out.RequestID
}

// waitForRequest polls the dashboard API until the request leaves the
// pending/approved/running states, then returns it.
func waitForRequest(t *testing.T, s *TestServer, requestID string) RequestResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body := WebGet(t, s.WebURL()+"/api/requests", s.token)
		if code != 200 {
			t.Fatalf("list requests: status %d", code)
		}
		var list []RequestResult
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("parse requests: %v\nraw: %s", err, body)
		}
		for _, r := range list {
			if r.ID != requestID {
				continue
			}
			switch r.Status {
			case "pending", "approved", "running":
			default:
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("request %s did not finish within 5s", requestID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// approveRequest approves a pending request through the dashboard API and waits
// for it to finish. It fails the test unless the request completed cleanly.
func approveRequest(t *testing.T, s *TestServer, requestID string) RequestResult {
	t.Helper()
	code, body := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("approve %s: status %d, body %s", requestID, code, body)
	}
	done := waitForRequest(t, s, requestID)
	if done.Status != "complete" {
		t.Fatalf("request %s ended %s: %+v", requestID, done.Status, done.Result)
	}
	return done
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

// registerContainer registers a container end-to-end: queue via MCP, approve in
// the dashboard, wait for the registry write. register_container is
// approval-gated (finding #23 part 2), so calling the tool alone leaves the
// registry empty.
func registerContainer(t *testing.T, s *TestServer, c *MCPClient, callID int, ctid float64, ip, hostname string, hasRelaySSH bool) Container {
	t.Helper()
	return registerContainerArgs(t, s, c, callID, map[string]interface{}{
		"ctid": ctid, "ip": ip, "hostname": hostname, "has_relay_ssh": hasRelaySSH,
	})
}

// registerContainerArgs is registerContainer with the full argument map, for
// tests that pass ssh_user or omit has_relay_ssh. Returns the Container the
// approved request wrote to the registry.
func registerContainerArgs(t *testing.T, s *TestServer, c *MCPClient, callID int, args map[string]interface{}) Container {
	t.Helper()
	resp := c.Call(t, callID, "tools/call", map[string]interface{}{
		"name": "register_container", "arguments": args,
	})
	done := approveRequest(t, s, extractPendingID(t, resp))
	if done.Result == nil {
		t.Fatalf("register_container produced no result: %+v", done)
	}
	var ct Container
	if err := json.Unmarshal([]byte(done.Result.Stdout), &ct); err != nil {
		t.Fatalf("parse registered container: %v\nraw: %s", err, done.Result.Stdout)
	}
	return ct
}

// listContainers calls list_containers and parses the registry contents.
func listContainers(t *testing.T, c *MCPClient, callID int) []Container {
	t.Helper()
	resp := c.Call(t, callID, "tools/call", map[string]interface{}{
		"name": "list_containers", "arguments": map[string]interface{}{},
	})
	return extractContainerList(t, resp)
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

// TestRegisterContainer drives finding #23 part 2 end-to-end: the tool returns
// a pending request instead of a Container, the registry stays untouched until
// the human approves, and the approved request is what writes the entry.
func TestRegisterContainer(t *testing.T) {
	s, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid":          float64(133),
			"ip":            "192.168.10.90",
			"hostname":      "archivebox",
			"has_relay_ssh": true,
		},
	})

	requestID := extractPendingID(t, resp)

	// Before approval the registry is empty.
	if list := listContainers(t, c, 3); len(list) != 0 {
		t.Fatalf("registry mutated before approval: %+v", list)
	}

	approveRequest(t, s, requestID)

	list := listContainers(t, c, 4)
	if len(list) != 1 {
		t.Fatalf("expected 1 container after approval, got %d", len(list))
	}
	ct := list[0]
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

// TestRegisterContainerDeniedLeavesRegistryEmpty is the control for the test
// above: an approval click is what writes the registry, so a denial must leave
// it exactly as it was.
func TestRegisterContainerDeniedLeavesRegistryEmpty(t *testing.T) {
	s, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_container",
		"arguments": map[string]interface{}{
			"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox",
		},
	})
	requestID := extractPendingID(t, resp)

	code, body := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), requestID),
		s.token, map[string]string{"reason": "not this one"})
	if code != 200 {
		t.Fatalf("deny returned status %d, body %s", code, body)
	}

	if list := listContainers(t, c, 3); len(list) != 0 {
		t.Fatalf("denied register_container still wrote the registry: %+v", list)
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
	s, c := initClient(t)

	// Register initial
	registerContainerArgs(t, s, c, 2, map[string]interface{}{
		"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox",
	})

	// Update same CTID
	ct := registerContainerArgs(t, s, c, 3, map[string]interface{}{
		"ctid":          float64(133),
		"ip":            "192.168.10.91",
		"hostname":      "archivebox-v2",
		"has_relay_ssh": true,
	})

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
	if list := listContainers(t, c, 4); len(list) != 1 {
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
	s, c := initClient(t)

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
		registerContainerArgs(t, s, c, i+2, map[string]interface{}{
			"ctid": ct.ctid, "ip": ct.ip, "hostname": ct.hostname,
		})
	}

	list := listContainers(t, c, 5)
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

	registerContainer(t, s, c, 2, 133, "192.168.10.90", "archivebox", true)

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

	registerContainer(t, s, c, 2, 133, "192.168.10.90", "archivebox", false)

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

	registerContainer(t, s, c, 2, 133, "192.168.10.90", "archivebox", true)

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

	registerContainer(t, s, c, 2, 133, "192.168.10.90", "archivebox", true)

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

	registerContainer(t, s, c, 2, 133, "192.168.10.90", "archivebox", false)

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

	registerContainer(t, s, c, 2, 133, "192.168.10.90", "archivebox", true)

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

	registerContainer(t, s1, c1, 2, 133, "192.168.10.90", "archivebox", true)

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
	s, c := initClient(t)

	ct := registerContainerArgs(t, s, c, 2, map[string]interface{}{
		"ctid":          float64(9999),
		"ip":            "192.168.10.104",
		"hostname":      "corsair",
		"has_relay_ssh": true,
		"ssh_user":      "Lara Duong",
	})

	if ct.SSHUser != "Lara Duong" {
		t.Errorf("expected ssh_user 'Lara Duong', got %q", ct.SSHUser)
	}
}

func TestExecContainerCustomSSHUser(t *testing.T) {
	s, c := initClient(t)

	// Register with custom ssh_user
	registerContainerArgs(t, s, c, 2, map[string]interface{}{
		"ctid":          float64(9999),
		"ip":            "192.168.10.104",
		"hostname":      "corsair",
		"has_relay_ssh": true,
		"ssh_user":      "Lara Duong",
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
	registerContainer(t, s, c, 2, 133, "192.168.10.90", "archivebox", true)

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
	s, c := initClient(t)

	// Register with ssh_user
	registerContainerArgs(t, s, c, 2, map[string]interface{}{
		"ctid":          float64(9999),
		"ip":            "192.168.10.104",
		"hostname":      "corsair",
		"has_relay_ssh": true,
		"ssh_user":      "Lara Duong",
	})

	// Upsert without ssh_user — should preserve existing value
	ct := registerContainerArgs(t, s, c, 3, map[string]interface{}{
		"ctid":          float64(9999),
		"ip":            "192.168.10.105",
		"hostname":      "corsair-v2",
		"has_relay_ssh": true,
	})

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

	registerContainerArgs(t, s1, c1, 2, map[string]interface{}{
		"ctid":          float64(9999),
		"ip":            "192.168.10.104",
		"hostname":      "corsair",
		"has_relay_ssh": true,
		"ssh_user":      "Lara Duong",
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

// --- registry approval-gate tests (finding #23 part 2) ---
//
// register_container, delete_container, register_machine and delete_machine
// used to mutate the registry synchronously from the MCP port, with no human
// in the loop. They now queue a "registry_op" request like every other mutating
// tool; these tests drive the full queue → approve → mutate path.

// listMachines calls list_machines and parses the registry contents.
func listMachines(t *testing.T, c *MCPClient, callID int) []Machine {
	t.Helper()
	resp := c.Call(t, callID, "tools/call", map[string]interface{}{
		"name": "list_machines", "arguments": map[string]interface{}{},
	})
	var list []Machine
	text := toolText(t, resp)
	if err := json.Unmarshal([]byte(text), &list); err != nil {
		t.Fatalf("failed to parse machine list: %v\nraw: %s", err, text)
	}
	return list
}

func TestDeleteContainerRequiresApproval(t *testing.T) {
	s, c := initClient(t)

	registerContainer(t, s, c, 2, 133, "192.168.10.90", "archivebox", true)

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name":      "delete_container",
		"arguments": map[string]interface{}{"ctid": float64(133)},
	})
	requestID := extractPendingID(t, resp)

	// Still registered until the human approves.
	if list := listContainers(t, c, 4); len(list) != 1 {
		t.Fatalf("expected the container to survive until approval, got %+v", list)
	}

	done := approveRequest(t, s, requestID)
	if !strings.Contains(done.Result.Stdout, `"deleted":true`) {
		t.Errorf("expected deleted:true in result stdout, got %q", done.Result.Stdout)
	}

	if list := listContainers(t, c, 5); len(list) != 0 {
		t.Fatalf("expected empty registry after approved delete, got %+v", list)
	}
}

// TestDeleteContainerUnregisteredFailsAfterApproval pins where the "not found"
// error moved to: the MCP call can no longer know, so the failure surfaces on
// the approved request instead of at submission time.
func TestDeleteContainerUnregisteredFailsAfterApproval(t *testing.T) {
	s, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name":      "delete_container",
		"arguments": map[string]interface{}{"ctid": float64(999)},
	})
	requestID := extractPendingID(t, resp)

	code, body := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 200 {
		t.Fatalf("approve returned status %d, body %s", code, body)
	}

	done := waitForRequest(t, s, requestID)
	if done.Status != "error" {
		t.Fatalf("expected status error, got %s (%+v)", done.Status, done.Result)
	}
	if !strings.Contains(done.Result.Stderr, "not found") {
		t.Errorf("expected a not-found error on stderr, got %q", done.Result.Stderr)
	}
}

func TestRegisterMachineRequiresApproval(t *testing.T) {
	s, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_machine",
		"arguments": map[string]interface{}{
			"name":     "corsair-win",
			"host":     "100.106.181.59",
			"ssh_user": "esthie",
			"shell":    "powershell",
		},
	})
	requestID := extractPendingID(t, resp)

	if list := listMachines(t, c, 3); len(list) != 0 {
		t.Fatalf("machine registry mutated before approval: %+v", list)
	}

	approveRequest(t, s, requestID)

	list := listMachines(t, c, 4)
	if len(list) != 1 {
		t.Fatalf("expected 1 machine after approval, got %d", len(list))
	}
	if list[0].Name != "corsair-win" || list[0].Host != "100.106.181.59" {
		t.Errorf("unexpected machine: %+v", list[0])
	}
	if list[0].SSHUser != "esthie" {
		t.Errorf("expected ssh_user esthie, got %q", list[0].SSHUser)
	}
	if list[0].Shell != "powershell" {
		t.Errorf("expected shell powershell, got %q", list[0].Shell)
	}
}

func TestDeleteMachineRequiresApproval(t *testing.T) {
	s, c := initClient(t)

	regResp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "register_machine",
		"arguments": map[string]interface{}{
			"name": "corsair-win", "host": "100.106.181.59", "ssh_user": "esthie",
		},
	})
	approveRequest(t, s, extractPendingID(t, regResp))

	delResp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name":      "delete_machine",
		"arguments": map[string]interface{}{"name": "corsair-win"},
	})
	requestID := extractPendingID(t, delResp)

	if list := listMachines(t, c, 4); len(list) != 1 {
		t.Fatalf("expected the machine to survive until approval, got %+v", list)
	}

	done := approveRequest(t, s, requestID)
	if !strings.Contains(done.Result.Stdout, `"deleted":true`) {
		t.Errorf("expected deleted:true in result stdout, got %q", done.Result.Stdout)
	}

	if list := listMachines(t, c, 5); len(list) != 0 {
		t.Fatalf("expected empty machine registry after approved delete, got %+v", list)
	}
}

// TestRegistryOpValidationStillRejectsAtSubmission confirms the arg validation
// that used to guard the synchronous path still runs before anything is queued
// — a malformed call must never reach the approval queue.
func TestRegistryOpValidationStillRejectsAtSubmission(t *testing.T) {
	s, c := initClient(t)

	cases := []struct {
		name string
		tool string
		args map[string]interface{}
	}{
		{"container missing ip", "register_container", map[string]interface{}{
			"ctid": float64(133), "hostname": "archivebox",
		}},
		{"container option-injectable ssh_user", "register_container", map[string]interface{}{
			"ctid": float64(133), "ip": "192.168.10.90", "hostname": "archivebox",
			"ssh_user": "-oProxyCommand=id",
		}},
		{"machine bad name", "register_machine", map[string]interface{}{
			"name": "a/b", "host": "1.2.3.4", "ssh_user": "esthie",
		}},
		{"machine bad shell", "register_machine", map[string]interface{}{
			"name": "m", "host": "1.2.3.4", "ssh_user": "esthie", "shell": "fish",
		}},
		{"delete_machine missing name", "delete_machine", map[string]interface{}{}},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := c.Call(t, i+2, "tools/call", map[string]interface{}{
				"name": tc.tool, "arguments": tc.args,
			})
			if !isErrorResponse(resp) {
				t.Fatalf("expected a validation error, got: %s", toolText(t, resp))
			}
		})
	}

	// A rejected call must not leave anything in the approval queue.
	code, body := WebGet(t, s.WebURL()+"/api/requests", s.token)
	if code != 200 {
		t.Fatalf("list requests: status %d", code)
	}
	var queued []RequestResult
	if err := json.Unmarshal(body, &queued); err != nil {
		t.Fatalf("parse requests: %v\nraw: %s", err, body)
	}
	if len(queued) != 0 {
		t.Fatalf("expected an empty approval queue, got %d requests", len(queued))
	}
}
