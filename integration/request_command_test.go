package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

// toolResultText pulls the text out of a tools/call response (success or error).
func toolResultText(t *testing.T, resp *JSONRPCResponse) string {
	t.Helper()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Content) == 0 {
		t.Fatalf("no content in response: %s", string(resp.Result))
	}
	return result.Content[0].Text
}

// TestRequestCommandToolsInList pins that all THREE names are exposed. The
// retired one stays listed deliberately: a caller still holding the old name
// must get the explanation of where its command would have run. Drop it from
// the list and the answer becomes `unknown tool`, which explains nothing.
func TestRequestCommandToolsInList(t *testing.T) {
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

	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"request_command", "request_command_for_relay", "request_command_for_host"} {
		if !names[want] {
			t.Errorf("%s not found in tools/list", want)
		}
	}
}

// TestRequestCommandRetiredRejectedEndToEnd: the old name is rejected loudly,
// and the message says where the call would have run - the information its
// callers never had.
func TestRequestCommandRetiredRejectedEndToEnd(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command",
		"arguments": map[string]interface{}{
			"command": "hostname",
			"reason":  "where am I",
		},
	})
	if !isErrorResponse(resp) {
		t.Fatalf("request_command must be rejected, got: %s", toolResultText(t, resp))
	}
	msg := toolResultText(t, resp)
	for _, want := range []string{"request_command_for_relay", "request_command_for_host", "RELAY CONTAINER"} {
		if !strings.Contains(msg, want) {
			t.Errorf("rejection must mention %q; got: %s", want, msg)
		}
	}
}

// TestRequestCommandForHostBuildsTheSSHWrapperEndToEnd: the host route's
// contract at the wire level - the stored request is an ssh invocation whose
// argv carries the caller's command intact, and whose approval reason names the
// destination so the reviewer decides knowing where it runs.
func TestRequestCommandForHostBuildsTheSSHWrapperEndToEnd(t *testing.T) {
	_, c := initClient(t)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command_for_host",
		"arguments": map[string]interface{}{
			"command": "df",
			"args":    []interface{}{"-h", "/"},
			"reason":  "check disk",
		},
	})
	if isErrorResponse(resp) {
		t.Fatalf("unexpected error: %s", toolResultText(t, resp))
	}

	var created struct {
		RequestID string `json:"request_id"`
	}
	json.Unmarshal([]byte(toolResultText(t, resp)), &created)
	if created.RequestID == "" {
		t.Fatal("expected a request_id")
	}

	req := findRequestByID(t, c, 3, created.RequestID)
	if req.Command != "ssh" {
		t.Errorf("host route must store an ssh command, got %q", req.Command)
	}
	got := strings.Join(req.Args, " ")
	want := "root@192.168.10.50 -- 'df' '-h' '/'"
	if !strings.HasSuffix(got, want) {
		t.Errorf("host route argv must end with %q, got %q", want, got)
	}
	if !strings.Contains(req.Reason, "HOST 192.168.10.50") {
		t.Errorf("approval reason must name the destination, got %q", req.Reason)
	}
}
