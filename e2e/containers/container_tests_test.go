package containers_e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestExecOnEachNode verifies that exec_container routes commands to the
// correct container by checking the hostname in each.
func TestExecOnEachNode(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	// Register all 3 nodes
	for i, n := range env.nodes {
		registerNode(t, c, 9001+i, n.ip, "test-node-"+fmt.Sprintf("%d", i+1), 100+i)
	}

	// Execute hostname on each and verify
	for i, n := range env.nodes {
		t.Run(n.name, func(t *testing.T) {
			callBase := 200 + i*10
			resp := c.call(t, callBase, "tools/call", map[string]interface{}{
				"name": "exec_container",
				"arguments": map[string]interface{}{
					"ctid":    9001 + i,
					"command": "hostname",
					"reason":  "verify container identity",
				},
			})

			requestID := extractRequestID(t, resp)
			result := approveAndWait(t, c, s, requestID, callBase+1)

			if result.Status != "complete" {
				t.Fatalf("expected complete, got %s", result.Status)
			}
			expected := fmt.Sprintf("test-node-%d", i+1)
			got := strings.TrimSpace(result.Result.Stdout)
			if got != expected {
				t.Errorf("expected hostname %q, got %q", expected, got)
			}
		})
	}
}

// TestExecShellPipe verifies shell mode works through the container path,
// including pipes.
func TestExecShellPipe(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	registerNode(t, c, 9001, env.nodes[0].ip, "test-node-1", 100)

	resp := c.call(t, 200, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9001,
			"command": "echo hello world",
			"args":    []string{"| tr a-z A-Z"},
			"reason":  "test shell pipe",
			"shell":   true,
		},
	})

	requestID := extractRequestID(t, resp)
	result := approveAndWait(t, c, s, requestID, 201)

	if result.Status != "complete" {
		t.Fatalf("expected complete, got %s", result.Status)
	}
	got := strings.TrimSpace(result.Result.Stdout)
	if got != "HELLO WORLD" {
		t.Errorf("expected 'HELLO WORLD', got %q", got)
	}
}

// TestExecNonZeroExit verifies error status when command fails.
func TestExecNonZeroExit(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	registerNode(t, c, 9001, env.nodes[0].ip, "test-node-1", 100)

	resp := c.call(t, 200, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9001,
			"command": "false",
			"reason":  "test non-zero exit",
		},
	})

	requestID := extractRequestID(t, resp)
	result := approveAndWait(t, c, s, requestID, 201)

	if result.Status != "error" {
		t.Errorf("expected error status, got %s", result.Status)
	}
	if result.Result.ExitCode == 0 {
		t.Error("expected non-zero exit code")
	}
}

// TestExecFileRoundtrip writes a file to one container and reads it from
// the same container, verifying the content survives the trip.
func TestExecFileRoundtrip(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	registerNode(t, c, 9001, env.nodes[0].ip, "test-node-1", 100)

	content := "the quick brown fox jumps over the lazy dog"

	// Write via shell
	resp := c.call(t, 200, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9001,
			"command": fmt.Sprintf("echo '%s' > /tmp/e2e-test.txt", content),
			"reason":  "write test file",
			"shell":   true,
		},
	})
	requestID := extractRequestID(t, resp)
	result := approveAndWait(t, c, s, requestID, 201)
	if result.Status != "complete" {
		t.Fatalf("write failed: %s", result.Status)
	}

	// Read back
	resp = c.call(t, 210, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9001,
			"command": "cat",
			"args":    []string{"/tmp/e2e-test.txt"},
			"reason":  "read test file",
		},
	})
	requestID = extractRequestID(t, resp)
	result = approveAndWait(t, c, s, requestID, 211)

	if result.Status != "complete" {
		t.Fatalf("read failed: %s", result.Status)
	}
	got := strings.TrimSpace(result.Result.Stdout)
	if got != content {
		t.Errorf("expected %q, got %q", content, got)
	}
}

// TestExecIsolation verifies that a file created in node1 is NOT visible in
// node2, proving the containers are actually separate.
func TestExecIsolation(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	registerNode(t, c, 9001, env.nodes[0].ip, "test-node-1", 100)
	registerNode(t, c, 9002, env.nodes[1].ip, "test-node-2", 101)

	// Create a file on node1
	resp := c.call(t, 200, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9001,
			"command": "touch /tmp/isolation-proof",
			"reason":  "create isolation marker",
			"shell":   true,
		},
	})
	requestID := extractRequestID(t, resp)
	approveAndWait(t, c, s, requestID, 201)

	// Verify it exists on node1
	resp = c.call(t, 210, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9001,
			"command": "test -f /tmp/isolation-proof && echo exists",
			"reason":  "check marker on node1",
			"shell":   true,
		},
	})
	requestID = extractRequestID(t, resp)
	result := approveAndWait(t, c, s, requestID, 211)
	if strings.TrimSpace(result.Result.Stdout) != "exists" {
		t.Fatal("marker file not found on node1")
	}

	// Verify it does NOT exist on node2
	resp = c.call(t, 220, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9002,
			"command": "test -f /tmp/isolation-proof && echo exists || echo missing",
			"reason":  "check marker on node2",
			"shell":   true,
		},
	})
	requestID = extractRequestID(t, resp)
	result = approveAndWait(t, c, s, requestID, 221)
	got := strings.TrimSpace(result.Result.Stdout)
	if got != "missing" {
		t.Errorf("expected 'missing' on node2, got %q — containers are NOT isolated", got)
	}
}

// TestExecOnAllThreeNodes runs a command on all 3 nodes concurrently and
// collects the results, verifying each responded with its own hostname.
func TestExecOnAllThreeNodes(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	for i, n := range env.nodes {
		registerNode(t, c, 9001+i, n.ip, "test-node-"+fmt.Sprintf("%d", i+1), 100+i)
	}

	// Submit all 3 commands
	ids := make([]string, 3)
	for i := range env.nodes {
		resp := c.call(t, 300+i*10, "tools/call", map[string]interface{}{
			"name": "exec_container",
			"arguments": map[string]interface{}{
				"ctid":    9001 + i,
				"command": "hostname",
				"reason":  fmt.Sprintf("bulk exec node %d", i+1),
			},
		})
		ids[i] = extractRequestID(t, resp)
	}

	// Approve all 3
	for i, id := range ids {
		result := approveAndWait(t, c, s, id, 400+i*10)
		expected := fmt.Sprintf("test-node-%d", i+1)
		got := strings.TrimSpace(result.Result.Stdout)
		if got != expected {
			t.Errorf("node %d: expected hostname %q, got %q", i+1, expected, got)
		}
	}
}

// TestWhitelistAutoApprovesContainerExec verifies that whitelisted
// exec_container commands are auto-approved. The exec_container tool
// translates to "ssh root@<ip> -- <cmd>", so we whitelist the final
// SSH form.
func TestWhitelistAutoApprovesContainerExec(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	registerNode(t, c, 9001, env.nodes[0].ip, "test-node-1", 100)

	// The exec_container for "hostname" on node1 translates to:
	//   ssh -F <config> root@<ip> -- hostname
	// Whitelist that exact command.
	code, _ := webPost(t, s.webURL()+"/api/whitelist", s.token, map[string]interface{}{
		"command": "ssh",
		"args":    []string{"-F", s.sshConfigPath, fmt.Sprintf("root@%s", env.nodes[0].ip), "--", "hostname"},
	})
	if code != 200 {
		t.Fatalf("whitelist add returned %d", code)
	}

	// Now exec_container should auto-approve
	resp := c.call(t, 200, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9001,
			"command": "hostname",
			"reason":  "should auto-approve",
		},
	})
	requestID := extractRequestID(t, resp)

	// Poll — should complete without manual approval
	var result *requestResult
	for i := 0; i < 25; i++ {
		r := c.call(t, 300+i, "tools/call", map[string]interface{}{
			"name":      "get_result",
			"arguments": map[string]interface{}{"request_id": requestID},
		})
		result = extractToolResult(t, r)
		if result.Status == "complete" || result.Status == "error" {
			break
		}
		if i == 24 {
			t.Fatalf("auto-approve timed out, status: %s", result.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}

	if result.Status != "complete" {
		t.Fatalf("expected complete, got %s", result.Status)
	}
	got := strings.TrimSpace(result.Result.Stdout)
	if got != "test-node-1" {
		t.Errorf("expected 'test-node-1', got %q", got)
	}
}

// TestRequestCommandDirectSSH verifies request_command with direct SSH to a
// test container (not through exec_container).
func TestRequestCommandDirectSSH(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	resp := c.call(t, 200, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "ssh",
			"args":    []string{"-F", s.sshConfigPath, fmt.Sprintf("root@%s", env.nodes[0].ip), "uname", "-s"},
			"reason":  "test direct ssh",
		},
	})

	requestID := extractRequestID(t, resp)
	result := approveAndWait(t, c, s, requestID, 201)

	if result.Status != "complete" {
		stderr := ""
		if result.Result != nil {
			stderr = result.Result.Stderr
		}
		t.Fatalf("expected complete, got %s (stderr: %q)", result.Status, stderr)
	}
	got := strings.TrimSpace(result.Result.Stdout)
	if got != "Linux" {
		t.Errorf("expected 'Linux', got %q (stderr: %q, exit: %d)", got, result.Result.Stderr, result.Result.ExitCode)
	}
}

// TestExecTimeout verifies that a command that exceeds the timeout is killed.
func TestExecTimeout(t *testing.T) {
	s := startServer(t)
	c := newMCPClient(t, s.mcpURL())
	c.init(t)

	registerNode(t, c, 9001, env.nodes[0].ip, "test-node-1", 100)

	resp := c.call(t, 200, "tools/call", map[string]interface{}{
		"name": "exec_container",
		"arguments": map[string]interface{}{
			"ctid":    9001,
			"command": "sleep 60",
			"reason":  "test timeout",
			"shell":   true,
			"timeout": 2,
		},
	})

	requestID := extractRequestID(t, resp)
	result := approveAndWait(t, c, s, requestID, 201)

	if result.Result.ExitCode == 0 {
		t.Error("expected non-zero exit for timed-out command")
	}
	if !strings.Contains(result.Result.Stderr, "timed out") {
		t.Errorf("expected timeout message in stderr, got %q", result.Result.Stderr)
	}
}
