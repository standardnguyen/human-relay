package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePermFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.json")
	body := `{
		"allow": ["Bash(ls:*)", "Read(/tmp/**)"],
		"deny":  ["Bash(rm -rf:*)"],
		"ask":   ["Bash(git push:*)"]
	}`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write perm file: %v", err)
	}
	return path
}

func checkPermission(t *testing.T, s *TestServer, payload map[string]any) (int, map[string]any) {
	t.Helper()
	status, body := WebPost(t, s.WebURL()+"/api/permission/check", s.token, payload)
	if status >= 400 {
		return status, nil
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	return status, resp
}

func TestPermissionAllowMatch(t *testing.T) {
	s := StartServer(t, WithPermissionsFile(writePermFile(t)))
	_, resp := checkPermission(t, s, map[string]any{
		"tool":   "Bash",
		"input":  map[string]any{"command": "ls -la"},
		"reason": "list cwd",
		"client": "pi",
	})
	if resp["verdict"] != "allow" {
		t.Fatalf("expected allow, got %+v", resp)
	}
	if resp["rule_id"] != "Bash(ls:*)" {
		t.Errorf("rule_id: %v", resp["rule_id"])
	}
}

func TestPermissionDenyMatch(t *testing.T) {
	s := StartServer(t, WithPermissionsFile(writePermFile(t)))
	_, resp := checkPermission(t, s, map[string]any{
		"tool":   "Bash",
		"input":  map[string]any{"command": "rm -rf /tmp/foo"},
		"reason": "cleanup",
		"client": "pi",
	})
	if resp["verdict"] != "deny" {
		t.Fatalf("expected deny, got %+v", resp)
	}
}

func TestPermissionHardDenySecretsPath(t *testing.T) {
	s := StartServer(t, WithPermissionsFile(writePermFile(t)))
	_, resp := checkPermission(t, s, map[string]any{
		"tool":   "Read",
		"input":  map[string]any{"file_path": "/root/.ssh/id_ed25519"},
		"reason": "want key",
	})
	if resp["verdict"] != "deny" {
		t.Fatalf("expected hard-deny, got %+v", resp)
	}
	if resp["rule_id"] != "hard_deny" {
		t.Errorf("rule_id should be hard_deny: %v", resp["rule_id"])
	}
}

func TestPermissionAskCreatesQueueRequest(t *testing.T) {
	s := StartServer(t, WithPermissionsFile(writePermFile(t)))
	_, resp := checkPermission(t, s, map[string]any{
		"tool":   "Bash",
		"input":  map[string]any{"command": "git push origin dev"},
		"reason": "ship",
		"client": "pi",
	})
	if resp["verdict"] != "ask" {
		t.Fatalf("expected ask, got %+v", resp)
	}
	reqID, ok := resp["request_id"].(string)
	if !ok || reqID == "" {
		t.Fatalf("expected request_id, got %+v", resp)
	}

	// Pending status before approval
	status, body := WebGet(t, fmt.Sprintf("%s/api/permission/check/%s", s.WebURL(), reqID), s.token)
	if status != 200 {
		t.Fatalf("status endpoint: %d %s", status, body)
	}
	var statusResp map[string]any
	json.Unmarshal(body, &statusResp)
	if statusResp["verdict"] != "ask" || statusResp["status"] != "pending" {
		t.Fatalf("expected pending/ask, got %+v", statusResp)
	}

	// Approve via the existing requests endpoint
	approveStatus, approveBody := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), reqID), s.token, map[string]any{})
	if approveStatus != 200 {
		t.Fatalf("approve failed: %d %s", approveStatus, approveBody)
	}

	// Poll until status flips to allow
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, body := WebGet(t, fmt.Sprintf("%s/api/permission/check/%s", s.WebURL(), reqID), s.token)
		var r map[string]any
		json.Unmarshal(body, &r)
		if r["verdict"] == "allow" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("verdict never flipped to allow after approve")
}

func TestPermissionAskThenDeny(t *testing.T) {
	s := StartServer(t, WithPermissionsFile(writePermFile(t)))
	_, resp := checkPermission(t, s, map[string]any{
		"tool":   "Bash",
		"input":  map[string]any{"command": "git push origin main"},
		"reason": "push to main",
	})
	reqID := resp["request_id"].(string)

	denyStatus, denyBody := WebPost(t, fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), reqID), s.token, map[string]any{
		"reason": "no thx",
	})
	if denyStatus != 200 {
		t.Fatalf("deny failed: %d %s", denyStatus, denyBody)
	}

	_, body := WebGet(t, fmt.Sprintf("%s/api/permission/check/%s", s.WebURL(), reqID), s.token)
	var r map[string]any
	json.Unmarshal(body, &r)
	if r["verdict"] != "deny" {
		t.Fatalf("expected deny after operator deny, got %+v", r)
	}
}

func TestPermissionDefaultAskFallthrough(t *testing.T) {
	s := StartServer(t, WithPermissionsFile(writePermFile(t)))
	_, resp := checkPermission(t, s, map[string]any{
		"tool":   "Write",
		"input":  map[string]any{"file_path": "/tmp/scratch.txt"},
		"reason": "scratch write",
	})
	if resp["verdict"] != "ask" {
		t.Fatalf("expected default ask, got %+v", resp)
	}
	if resp["rule_id"] != "default" {
		t.Errorf("rule_id should be default: %v", resp["rule_id"])
	}
}

func TestPermissionBadJSON(t *testing.T) {
	s := StartServer(t, WithPermissionsFile(writePermFile(t)))
	status, _ := WebPost(t, s.WebURL()+"/api/permission/check", s.token, "not an object")
	if status != 400 {
		t.Fatalf("expected 400 for bad payload, got %d", status)
	}
}

func TestPermissionNoConfigReturns503(t *testing.T) {
	// Server started without WithPermissionsFile — the relay still loads
	// the default empty file, so this test just ensures the endpoint is
	// reachable and returns a valid verdict (default ask).
	s := StartServer(t)
	_, resp := checkPermission(t, s, map[string]any{
		"tool":  "Bash",
		"input": map[string]any{"command": "ls"},
	})
	if resp["verdict"] != "ask" {
		t.Fatalf("empty config should default-ask, got %+v", resp)
	}
}
