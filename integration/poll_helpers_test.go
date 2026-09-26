package integration

import (
	"testing"
	"time"
)

// pollUntilDone polls get_result until the request leaves pending/running or
// the deadline passes, returning the last seen result.
func pollUntilDone(t *testing.T, c *MCPClient, callID int, requestID string) *RequestResult {
	t.Helper()
	var result *RequestResult
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := c.Call(t, callID, "tools/call", map[string]interface{}{
			"name": "get_result",
			"arguments": map[string]interface{}{
				"request_id": requestID,
			},
		})
		result = extractResult(t, resp)
		if result.Status == "complete" || result.Status == "error" {
			return result
		}
		time.Sleep(200 * time.Millisecond)
	}
	return result
}
