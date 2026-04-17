package integration

import (
	"encoding/json"
	"fmt"
	"testing"
)

// submitPendingCommand submits a request_command and returns the pending
// request's ID. Used by the withdraw tests to get a pending request without
// depending on approval or execution machinery.
func submitPendingCommand(t *testing.T, c *MCPClient, callID int, reason string) string {
	t.Helper()
	resp := c.Call(t, callID, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"test"},
			"reason":  reason,
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error submitting command")
	}
	return extractRequestID(t, resp)
}

func TestWithdrawRequestInToolsList(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/list", nil)
	if resp.Error != nil {
		t.Fatalf("tools/list returned error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	json.Unmarshal(resp.Result, &result)

	found := false
	for _, tool := range result.Tools {
		if tool.Name == "withdraw_request" {
			found = true
			break
		}
	}
	if !found {
		t.Error("withdraw_request not found in tools/list")
	}
}

func TestWithdrawRequestHappyPath(t *testing.T) {
	_, c := initClient(t)

	requestID := submitPendingCommand(t, c, 2, "will withdraw")

	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "withdraw_request",
		"arguments": map[string]interface{}{
			"request_id": requestID,
			"reason":     "changed my mind — wrong host",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error from withdraw_request")
	}

	found := findRequestByID(t, c, 4, requestID)
	if found.Status != "withdrawn" {
		t.Errorf("expected status withdrawn, got %q", found.Status)
	}
	if found.WithdrawReason != "changed my mind — wrong host" {
		t.Errorf("expected withdraw_reason preserved, got %q", found.WithdrawReason)
	}
}

func TestWithdrawRequestMissingArgs(t *testing.T) {
	_, c := initClient(t)

	tests := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing request_id", map[string]interface{}{
			"reason": "oops",
		}},
		{"missing reason", map[string]interface{}{
			"request_id": "abc123",
		}},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := c.Call(t, i+2, "tools/call", map[string]interface{}{
				"name":      "withdraw_request",
				"arguments": tt.args,
			})
			if !isErrorResponse(resp) {
				t.Fatal("expected error for missing field")
			}
		})
	}
}

func TestWithdrawRequestUnknownID(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "withdraw_request",
		"arguments": map[string]interface{}{
			"request_id": "0000000000000000",
			"reason":     "test unknown",
		},
	})
	if !isErrorResponse(resp) {
		t.Fatal("expected error for unknown request_id")
	}
}

func TestWithdrawRequestAfterDeny(t *testing.T) {
	s, c := initClient(t)

	requestID := submitPendingCommand(t, c, 2, "deny then withdraw")

	// Deny first
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), requestID),
		s.token, map[string]string{"reason": "denied"})
	if code != 200 {
		t.Fatalf("deny returned %d", code)
	}

	// Now try to withdraw — must fail, already decided
	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "withdraw_request",
		"arguments": map[string]interface{}{
			"request_id": requestID,
			"reason":     "too late",
		},
	})
	if !isErrorResponse(resp) {
		t.Fatal("expected error when withdrawing an already-decided request")
	}

	// Status should still be denied
	found := findRequestByID(t, c, 4, requestID)
	if found.Status != "denied" {
		t.Errorf("expected status still denied, got %q", found.Status)
	}
}

func TestWithdrawRequestBlocksApproval(t *testing.T) {
	s, c := initClient(t)

	requestID := submitPendingCommand(t, c, 2, "race: withdraw wins")

	// Withdraw
	resp := c.Call(t, 3, "tools/call", map[string]interface{}{
		"name": "withdraw_request",
		"arguments": map[string]interface{}{
			"request_id": requestID,
			"reason":     "retracted",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error from withdraw_request")
	}

	// Attempt approve — should fail with 409 (request not pending)
	code, _ := WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), requestID),
		s.token, nil)
	if code != 409 {
		t.Errorf("expected approve to return 409 after withdrawal, got %d", code)
	}

	found := findRequestByID(t, c, 4, requestID)
	if found.Status != "withdrawn" {
		t.Errorf("expected status still withdrawn, got %q", found.Status)
	}
}
