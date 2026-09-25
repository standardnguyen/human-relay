package integration

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The tool-call gate (/api/permission/check) was deleted: it had no caller
// once pi was uninstalled, and while it stayed, any client token could put
// an "ask" card into the approval queue under a client name it chose. These
// tests pin that the routes stay gone and that nothing an agent sends to them
// reaches the queue.

// TestToolGateRemoved_CheckRouteIsGone: a POST that used to fall through to
// the fail-closed "ask" verdict and queue a card now answers 404.
func TestToolGateRemoved_CheckRouteIsGone(t *testing.T) {
	s := StartServer(t)
	code, body := WebPost(t, s.WebURL()+"/api/permission/check", testToken, map[string]any{
		"tool":   "Bash",
		"input":  map[string]any{"command": "git push origin main"},
		"client": "injected",
	})
	if code != http.StatusNotFound {
		t.Fatalf("POST /api/permission/check: status %d, body %s, want 404 (the gate route still exists)", code, body)
	}
}

// TestToolGateRemoved_StatusRouteIsGone: the poll route answers 404 too,
// even for the id of a request that exists.
func TestToolGateRemoved_StatusRouteIsGone(t *testing.T) {
	s := StartServer(t)
	c := NewMCPClient(t, s.MCPURL())
	initMCP(t, c)
	rid := submitPending(t, c, 2, "exists")
	code, body := WebGet(t, s.WebURL()+"/api/permission/check/"+rid, testToken)
	if code != http.StatusNotFound {
		t.Fatalf("GET /api/permission/check/%s: status %d, body %s, want 404 (the gate poll route still exists)", rid, code, body)
	}
}

// TestToolGateRemoved_NothingReachesTheQueue: after an agent token posts to
// the old route, the approval queue is still empty.
func TestToolGateRemoved_NothingReachesTheQueue(t *testing.T) {
	s := StartServer(t)
	WebPost(t, s.WebURL()+"/api/permission/check", testToken, map[string]any{
		"tool":  "Bash",
		"input": map[string]any{"command": "git push origin main"},
	})
	code, body := WebGet(t, s.WebURL()+"/api/requests", testToken)
	if code != http.StatusOK {
		t.Fatalf("GET /api/requests: status %d, body %s", code, body)
	}
	var reqs []map[string]any
	if err := json.Unmarshal(body, &reqs); err != nil {
		t.Fatalf("decode /api/requests: %v (body %s)", err, body)
	}
	for _, r := range reqs {
		t.Errorf("queue holds a request posted through the removed gate: type=%v command=%v status=%v", r["type"], r["display_command"], r["status"])
	}
}
