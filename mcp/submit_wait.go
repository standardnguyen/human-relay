package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/standardnguyen/human-relay/store"
)

// Submit-and-wait. Every tool that queues a request for approval takes an
// optional `wait` (seconds). With wait > 0 the call is held until the request
// is decided and has run, and then answers with get_result's payload, so an
// agent needs one call instead of a submit plus a get_result. If the wait runs
// out first it answers with the ordinary pending response.
//
// The wait only changes WHEN the response is sent. It reads the store and
// nothing else: approval, the cooldown, the whitelist and gating all happen
// exactly as they do for a call without it.

// maxSubmitWait is the ceiling on `wait`; a larger value is clamped, not
// refused. It sits below the 60 s default request timeout of the MCP
// TypeScript SDK (DEFAULT_REQUEST_TIMEOUT_MSEC = 60000), which the SDK-built
// clients and the mcp-remote bridge inherit: a call held past its client's
// timeout fails on the client side even though the relay answers, and the
// agent loses the request ID it needed to recover. The 10 s margin covers the
// submission's own work (write_file probes the target over SSH first) and the
// response's trip back. A human approval can take minutes; that case is what
// the expiry response and get_result are for, not a longer hold.
const maxSubmitWait = 50 * time.Second

// submitWaitPoll is how often a held call re-reads its request. Polling keeps
// the wait inside the handler's own goroutine, so it cannot outlive the call.
const submitWaitPoll = 100 * time.Millisecond

// waitTools are the tools that queue a request for approval and answer with a
// request ID. One set drives both the schema (init below) and the handler
// (HandleContext), so the tools that advertise wait and the tools that honour
// it cannot drift apart.
var waitTools = map[string]bool{
	"request_command_for_relay": true,
	"request_command_for_host":  true,
	"exec_container":            true,
	"exec_machine":              true,
	"write_file":                true,
	"http_request":              true,
	"run_script":                true,
	"create_script":             true,
	"create_then_run":           true,
	"install_relay_ssh":         true,
	"install_ssh_key":           true,
	"register_container":        true,
	"delete_container":          true,
	"register_machine":          true,
	"delete_machine":            true,
}

func waitProperty() Property {
	return Property{
		Type: "integer",
		Description: fmt.Sprintf("Optional. Seconds to hold this call until the request is decided and has run, then return get_result's payload in this same call. "+
			"0 (default) returns the request ID at once. Capped server-side at %d s (larger values are clamped); "+
			"if the wait runs out first, the usual pending response comes back with wait_expired: true, so poll get_result with the request ID. "+
			"It does not approve anything: an unapproved request still waits for a human.", int(maxSubmitWait/time.Second)),
	}
}

func init() {
	for i := range ToolDefinitions {
		if waitTools[ToolDefinitions[i].Name] {
			ToolDefinitions[i].InputSchema.Properties["wait"] = waitProperty()
		}
	}
}

// maxWaitFromEnv reads MHR_MAX_WAIT (seconds). It can only lower the cap: the
// cap exists to stay under client timeouts, so a value above maxSubmitWait, or
// one that is missing, non-numeric or <= 0, means the default.
func maxWaitFromEnv() time.Duration {
	raw := os.Getenv("MHR_MAX_WAIT")
	if raw == "" {
		return maxSubmitWait
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs <= 0 {
		return maxSubmitWait
	}
	if d := time.Duration(secs) * time.Second; d < maxSubmitWait {
		return d
	}
	return maxSubmitWait
}

// parseWait validates `wait` before anything is queued, so a bad value never
// leaves the agent with a pending request it was told had failed.
func parseWait(args map[string]interface{}, max time.Duration) (time.Duration, *CallToolResult) {
	secs, errRes := intArgStrict(args, "wait")
	if errRes != nil {
		return 0, errRes
	}
	if secs < 0 {
		return 0, errorResult("wait must be >= 0 seconds")
	}
	// Clamp in seconds, before multiplying: from 9223372037 s up the product
	// overflows time.Duration and wraps negative. Strictly greater, so a cap
	// under a second still leaves wait=0 meaning "don't wait".
	if secs > int(max/time.Second) {
		return max, nil
	}
	return time.Duration(secs) * time.Second, nil
}

// settled reports whether a request has reached a state it will not leave:
// decided and, if approved, finished running.
func settled(s store.Status) bool {
	switch s {
	case store.StatusPending, store.StatusApproved, store.StatusRunning:
		return false
	}
	return true
}

// Handle runs one tool call with no caller context: a `wait` ends only when the
// request settles or the wait runs out.
func (h *ToolHandler) Handle(name string, args map[string]interface{}, client string) *CallToolResult {
	return h.HandleContext(context.Background(), name, args, client)
}

// HandleContext is Handle with the caller's context: a held `wait` stops when
// ctx is done (the client went away). The request itself is never touched by
// that, it stays in the store to be approved and read with get_result.
func (h *ToolHandler) HandleContext(ctx context.Context, name string, args map[string]interface{}, client string) *CallToolResult {
	if !waitTools[name] {
		return h.dispatchTool(name, args, client)
	}
	// The deadline counts from the call's arrival, so time spent submitting
	// comes out of the wait instead of pushing the reply past the cap.
	arrived := time.Now()
	wait, errRes := parseWait(args, h.maxWait)
	if errRes != nil {
		return errRes
	}
	res := h.dispatchTool(name, args, client)
	if wait <= 0 || res.IsError {
		return res
	}
	return h.awaitSettled(ctx, res, arrived.Add(wait), wait)
}

// awaitSettled holds a submission's response until its request settles, the
// deadline passes, or ctx ends. It owns no goroutine: the timer and ticker are
// stopped on every return path.
func (h *ToolHandler) awaitSettled(ctx context.Context, submitted *CallToolResult, deadline time.Time, wait time.Duration) *CallToolResult {
	if len(submitted.Content) == 0 {
		return submitted
	}
	var sub map[string]json.RawMessage
	if err := json.Unmarshal([]byte(submitted.Content[0].Text), &sub); err != nil {
		return submitted
	}
	var id string
	if err := json.Unmarshal(sub["request_id"], &id); err != nil || id == "" {
		return submitted
	}

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	ticker := time.NewTicker(submitWaitPoll)
	defer ticker.Stop()

	for {
		r := h.store.Get(id)
		if r == nil {
			return submitted
		}
		if settled(r.Status) {
			return settledResult(r, sub)
		}
		select {
		case <-ctx.Done():
			// Nobody is left to read the reply; the request is unaffected.
			return submitted
		case <-timer.C:
			if r = h.store.Get(id); r != nil && settled(r.Status) {
				return settledResult(r, sub)
			}
			return expiredResult(sub, r, wait)
		case <-ticker.C:
		}
	}
}

// settledResult is get_result's payload for r (through requestResult, so
// gating applies exactly as it does there), plus a `submission` object holding
// whatever the submit response carried besides request_id and status (shell
// warnings, write_file's target and route, ...). Those are shown only at
// submission, so without this a waiting caller would never see them.
func settledResult(r *store.Request, sub map[string]json.RawMessage) *CallToolResult {
	res := requestResult(r)
	extras := map[string]json.RawMessage{}
	for k, v := range sub {
		if k != "request_id" && k != "status" {
			extras[k] = v
		}
	}
	if len(extras) == 0 {
		return res
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(res.Content[0].Text), &payload); err != nil {
		return res
	}
	payload["submission"], _ = json.Marshal(extras)
	data, _ := json.Marshal(payload)
	return textResult(string(data))
}

// expiredResult is the submission's own pending response, marked so the caller
// can tell a wait that ran out from a relay that ignored `wait`. `status`
// stays "pending" (not yet resolved, poll get_result); current_status says
// where the request actually is, which may be approved or running.
func expiredResult(sub map[string]json.RawMessage, r *store.Request, wait time.Duration) *CallToolResult {
	out := map[string]interface{}{}
	for k, v := range sub {
		out[k] = v
	}
	out["wait_expired"] = true
	out["wait_seconds"] = int(wait / time.Second)
	if r != nil {
		out["current_status"] = r.Status
	}
	data, _ := json.Marshal(out)
	return textResult(string(data))
}
