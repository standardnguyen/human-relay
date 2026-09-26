package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/store"
)

// A finished request reads as gated only when its result has something to
// withhold. The web handler records a finished request with one store update,
// SetResult; an empty result used to come out of it still gated and was only
// released by a second update, so an agent reading in between got a 0-byte
// gated placeholder and waited for a Release click that was never needed.
// Both ways an agent reads a finished request are covered: get_result, and a
// submit-and-wait `wait` that returns once the request settles.
func TestFinishedGatedRequestReadsGatedOnlyWithOutput(t *testing.T) {
	cases := []struct {
		name      string
		result    store.Result
		wantGated bool
	}{
		{"empty", store.Result{ExitCode: 0}, false},
		{"stdout", store.Result{ExitCode: 0, Stdout: "withheld\n"}, true},
		{"stderr only", store.Result{ExitCode: 0, Stderr: "withheld\n"}, true},
	}
	submit := func(h *ToolHandler, wait float64) *CallToolResult {
		args := map[string]interface{}{"command": "true", "reason": "empty gated result test"}
		if wait > 0 {
			args["wait"] = wait
		}
		return h.Handle("request_command_for_relay", args, "")
	}
	// finish records the result the way the web handler does once a gated
	// approval has run.
	finish := func(s *store.Store, id string, r store.Result) {
		s.Approve(id, true)
		s.SetResult(id, &r, store.StatusComplete)
	}
	check := func(t *testing.T, m map[string]interface{}, wantGated bool) {
		t.Helper()
		if m["status"] != "complete" {
			t.Fatalf("status = %v, want complete: %v", m["status"], m)
		}
		gated, _ := m["output_gated"].(bool)
		result, _ := m["result"].(map[string]interface{})
		stdout, _ := result["stdout"].(string)
		placeholder := strings.HasPrefix(stdout, "[output gated")
		if gated != wantGated || placeholder != wantGated {
			t.Errorf("output_gated = %v, stdout = %q; want gated = %v (an agent told a finished request is gated waits for a Release click)",
				gated, stdout, wantGated)
		}
	}

	for _, c := range cases {
		t.Run(c.name+"/get_result", func(t *testing.T) {
			h := setup(t)
			id, _ := decode(t, submit(h, 0))["request_id"].(string)
			if id == "" {
				t.Fatal("no request_id")
			}
			finish(h.store, id, c.result)
			check(t, decode(t, h.Handle("get_result", map[string]interface{}{"request_id": id}, "")), c.wantGated)
		})
		t.Run(c.name+"/wait", func(t *testing.T) {
			h := setup(t)
			go func() {
				if eventually(3*time.Second, func() bool { return len(h.store.List("")) == 1 }) {
					finish(h.store, h.store.List("")[0].ID, c.result)
				}
			}()
			check(t, decode(t, submit(h, 5)), c.wantGated)
		})
	}
}
