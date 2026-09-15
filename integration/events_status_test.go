package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// eventFrame is the /events payload. The Part-2 keys are POINTERS so a test
// can tell "absent" from "present and empty" — the turbo pin turns on exactly
// that distinction, and the kapsrh side treats all three as optional.
type eventFrame struct {
	Type        string          `json:"type"`
	RequestID   string          `json:"request_id"`
	Status      *string         `json:"status"`
	Client      *string         `json:"client"`
	OutputGated *bool           `json:"output_gated"`
	Raw         json.RawMessage `json:"-"`
}

// subscribeEvents opens the deliberately-unauthenticated /events stream and
// returns frames as they are broadcast, plus a cleanup. The server registers
// the subscriber BEFORE writing response headers, so once http.Get returns no
// frame can be missed for being too early.
func subscribeEvents(t *testing.T, webURL string) (<-chan eventFrame, func()) {
	t.Helper()
	resp, err := http.Get(webURL + "/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("GET /events: status %d", resp.StatusCode)
	}
	frames := make(chan eventFrame, 128)
	stop := make(chan struct{})
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var f eventFrame
			if err := json.Unmarshal([]byte(data), &f); err != nil {
				continue
			}
			f.Raw = json.RawMessage(data)
			select {
			case frames <- f:
			case <-stop:
				return
			}
		}
	}()
	return frames, func() { close(stop) }
}

// waitForStatusFrame consumes until a frame for `id` reports `status`, and
// fails any frame naming the request that does not carry all three Part-2
// fields — that presence IS the contract this file pins.
func waitForStatusFrame(t *testing.T, frames <-chan eventFrame, id, status string, timeout time.Duration) eventFrame {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case f := <-frames:
			if f.RequestID != id {
				continue
			}
			if f.Status == nil {
				t.Fatalf("frame for %s carries no status (the key this test exists for): %s", id, f.Raw)
			}
			if f.Client == nil || *f.Client != "kapsrh-test" {
				t.Fatalf("frame for %s must name the submitting client kapsrh-test: %s", id, f.Raw)
			}
			if f.OutputGated == nil {
				t.Fatalf("frame for %s carries no output_gated: %s", id, f.Raw)
			}
			if *f.Status == status {
				return f
			}
		case <-deadline:
			t.Fatalf("no %q frame for request %s within %s", status, id, timeout)
		}
	}
}

// A request's state changes must reach /events subscribers with enough
// information to act on — status, and who submitted it — in the order they
// happened. Before this, every frame said only "something changed", so a
// subscriber had to turn around and re-fetch the whole list to find out what.
func TestEventsFramesCarryStatusAndClient(t *testing.T) {
	dataDir := t.TempDir()
	clientToken := mintClient(t, dataDir, "kapsrh-test")
	s := StartServer(t, WithDataDir(dataDir))

	frames, stop := subscribeEvents(t, s.WebURL())
	defer stop()

	c := NewMCPClientWithToken(t, s.MCPURL(), clientToken)
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "kapsrh-test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "request_command_for_relay",
		"arguments": map[string]interface{}{
			"command": "echo",
			"args":    []string{"events-status-test"},
			"reason":  "events status frame contract",
		},
	})
	id := extractRequestID(t, resp)

	code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve", s.WebURL(), id), s.token, map[string]interface{}{})
	if code != http.StatusOK {
		t.Fatalf("approve returned %d: %s", code, body)
	}

	// The three state changes the card names, in order. waitForStatusFrame
	// validates the fields on every frame it passes over.
	complete := waitForStatusFrame(t, frames, id, "approved", 15*time.Second)
	if complete.Status == nil || complete.Client == nil || complete.OutputGated == nil {
		t.Fatalf("approved frame incomplete: %s", complete.Raw)
	}
	waitForStatusFrame(t, frames, id, "running", 15*time.Second)
	done := waitForStatusFrame(t, frames, id, "complete", 15*time.Second)
	if *done.OutputGated {
		t.Fatalf("an ungated completion must say output_gated=false: %s", done.Raw)
	}
}

// The regression pin for the additive change: turbo frames pass the literal
// "on"/"off" as a request_id, so the lookup must miss and the frame must come
// out exactly as it always did — two keys, no status/client/output_gated.
func TestTurboFramesStayUnchanged(t *testing.T) {
	s := StartServer(t)
	frames, stop := subscribeEvents(t, s.WebURL())
	defer stop()

	code, body := WebPost(t, s.WebURL()+"/api/turbocharge", s.token, map[string]interface{}{
		"duration_minutes": 1,
		"cooldown_seconds": 1,
	})
	if code != http.StatusOK {
		t.Fatalf("turbocharge on returned %d: %s", code, body)
	}
	del, _ := http.NewRequest("DELETE", s.WebURL()+"/api/turbocharge", nil)
	del.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := http.DefaultClient.Do(del)
	if err != nil {
		t.Fatalf("turbocharge off: %v", err)
	}
	resp.Body.Close()

	for _, want := range []string{"on", "off"} {
		deadline := time.After(10 * time.Second)
		found := false
		for !found {
			select {
			case f := <-frames:
				if f.Type != "turbo" || f.RequestID != want {
					continue
				}
				if f.Status != nil || f.Client != nil || f.OutputGated != nil {
					t.Fatalf("turbo %q frame gained keys it must not have: %s", want, f.Raw)
				}
				var keys map[string]json.RawMessage
				json.Unmarshal(f.Raw, &keys)
				if len(keys) != 2 {
					t.Fatalf("turbo %q frame changed shape: %s", want, f.Raw)
				}
				found = true
			case <-deadline:
				t.Fatalf("no turbo %q frame within 10s", want)
			}
		}
	}
}
