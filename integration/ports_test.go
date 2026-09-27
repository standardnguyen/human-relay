package integration

import (
	"strings"
	"testing"
	"time"
)

// TestServersAliveAtOnceGetTheirOwnPorts: two relays running at the same time
// on one box must each bind ports of their own. s1 stands in for another
// suite's relay. While StartServer derived its ports from the pid, s2 got
// the same ones, died on "bind: address already in use", and its readiness
// probe was answered by s1.
func TestServersAliveAtOnceGetTheirOwnPorts(t *testing.T) {
	s1 := StartServer(t)
	s2 := StartServer(t)

	taken := map[int]bool{s1.mcpPort: true, s1.webPort: true}
	if taken[s2.mcpPort] || taken[s2.webPort] || s2.mcpPort == s2.webPort {
		time.Sleep(500 * time.Millisecond) // let s2 log why it exited
		t.Fatalf("s2 shares a port: s1 mcp=%d web=%d, s2 mcp=%d web=%d; s2 log:\n%s",
			s1.mcpPort, s1.webPort, s2.mcpPort, s2.webPort, s2.Stderr())
	}

	// Each server's URLs must reach that server: a request submitted to s2
	// is in s2's queue and not in s1's.
	c := NewMCPClient(t, s2.MCPURL())
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)
	id := extractRequestID(t, c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"ports"},
			"reason":  "ports test",
		},
	}))
	if _, body := WebGet(t, s2.WebURL()+"/api/requests", s2.token); !strings.Contains(string(body), id) {
		t.Fatalf("request %s is not in s2's queue; s2 log:\n%s", id, s2.Stderr())
	}
	if _, body := WebGet(t, s1.WebURL()+"/api/requests", s1.token); strings.Contains(string(body), id) {
		t.Fatalf("request %s submitted to s2 is in s1's queue; s2 log:\n%s", id, s2.Stderr())
	}
}
