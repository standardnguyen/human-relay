package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A gated http_request whose response has no body still has something to
// withhold: its response headers, which gating drops from every agent read. A
// 204 carrying Set-Cookie, or a 3xx carrying Location (redirects are never
// followed), comes back with empty stdout and stderr, and the empty-result
// auto-release used to take that as "nothing to withhold" and release it, so
// an agent read the headers through get_result, a submit-and-wait `wait`, or
// the web API.
//
// The header values are assembled at run time and never appear in a request's
// URL or arguments, so finding one in a response means a header leaked.
var (
	bodilessCookieMarker   = "GATEDCOOKIE" + "QQ"
	bodilessLocationMarker = "GATEDLOCATION" + "QQ"
)

func TestGatedBodilessHTTPResponseKeepsHeadersGated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/no-content", "/no-content-rule":
			w.Header().Set("Set-Cookie", "session="+bodilessCookieMarker)
			w.WriteHeader(http.StatusNoContent)
		case "/redirect":
			w.Header().Set("Location", "https://example.invalid/"+bodilessLocationMarker)
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	// A gate_output rule gates without a human: the call is auto-approved and
	// its output held for release.
	wlPath := filepath.Join(t.TempDir(), "whitelist.json")
	rules, _ := json.Marshal([]map[string]interface{}{
		{"command": "GET", "args": []string{upstream.URL + "/no-content-rule"}, "gate_output": true},
	})
	if err := os.WriteFile(wlPath, rules, 0644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	agentToken := mintClient(t, dataDir, "bodiless-reader")
	s := StartServer(t, WithDataDir(dataDir), WithApproverToken(approverToken), WithWhitelistFile(wlPath))
	c := NewMCPClient(t, s.MCPURL())
	initMCP(t, c)

	callID := 10
	nextID := func() int { callID++; return callID }
	submit := func(path string, wait int) *JSONRPCResponse {
		args := map[string]interface{}{"method": "GET", "url": upstream.URL + path, "reason": "gated bodiless response test"}
		if wait > 0 {
			args["wait"] = wait
		}
		return c.Call(t, nextID(), "tools/call", map[string]interface{}{"name": "http_request", "arguments": args})
	}

	// agentRead checks one agent-visible read of a finished request: it says
	// the output is gated, keeps the HTTP status code, and carries no header.
	agentRead := func(t *testing.T, read, text, marker string, wantStatus string, wantCode int) {
		t.Helper()
		if strings.Contains(text, marker) {
			t.Errorf("%s returned the gated response header %s before release: %s", read, marker, text)
		}
		var m bodilessRead
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			t.Fatalf("%s: not a request JSON: %v\n%s", read, err, text)
		}
		if m.Status != wantStatus || !m.OutputGated {
			t.Errorf("%s: status=%s output_gated=%v, want status=%s output_gated=true (a bodiless response still has headers to withhold)",
				read, m.Status, m.OutputGated, wantStatus)
		}
		if m.Result == nil || m.Result.StatusCode != wantCode {
			t.Errorf("%s: result %+v, want status_code %d kept visible", read, m.Result, wantCode)
		}
	}

	// webEntry returns request id's entry in the web API's list as token sees
	// it. Only that entry is checked: the list also holds requests an earlier
	// subtest released, which may carry the same header.
	webEntry := func(t *testing.T, token, id string) string {
		t.Helper()
		code, body := WebGet(t, s.WebURL()+"/api/requests", token)
		if code != http.StatusOK {
			t.Fatalf("GET /api/requests: status %d", code)
		}
		var list []json.RawMessage
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("GET /api/requests: %v", err)
		}
		for _, raw := range list {
			var r bodilessRead
			json.Unmarshal(raw, &r)
			if id != "" && r.ID == id {
				return string(raw)
			}
		}
		t.Fatalf("GET /api/requests has no request %q", id)
		return ""
	}

	// approverSees is the control: the header did reach the store, so an
	// agent read without it means it was withheld, not that it never existed.
	approverSees := func(t *testing.T, id, marker string) {
		t.Helper()
		if !strings.Contains(webEntry(t, approverToken, id), marker) {
			t.Fatalf("control failed: the approver's GET /api/requests has no %s for request %s, so the agent reads measured nothing", marker, id)
		}
	}

	cases := []struct {
		name, path, marker, status string
		code                       int
	}{
		{"204 with Set-Cookie", "/no-content", bodilessCookieMarker, "complete", http.StatusNoContent},
		{"302 with Location", "/redirect", bodilessLocationMarker, "error", http.StatusFound},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/approve-gated then get_result", func(t *testing.T) {
			id := extractRequestID(t, submit(tc.path, 0))
			if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/approve-gated", s.WebURL(), id), approverToken, nil); code != http.StatusOK {
				t.Fatalf("approve-gated: status %d, body %s", code, body)
			}
			waitForStatus(t, s, id, tc.status)
			approverSees(t, id, tc.marker)

			resp := c.Call(t, nextID(), "tools/call", map[string]interface{}{
				"name": "get_result", "arguments": map[string]interface{}{"request_id": id},
			})
			agentRead(t, "get_result", toolText(t, resp), tc.marker, tc.status, tc.code)
			agentRead(t, "agent GET /api/requests", webEntry(t, agentToken, id), tc.marker, tc.status, tc.code)

			// Release is the positive control for the read itself: once
			// released, the same get_result carries the header.
			if code, body := WebPost(t, fmt.Sprintf("%s/api/requests/%s/release", s.WebURL(), id), approverToken, nil); code != http.StatusOK {
				t.Fatalf("release: status %d, body %s", code, body)
			}
			released := toolText(t, c.Call(t, nextID(), "tools/call", map[string]interface{}{
				"name": "get_result", "arguments": map[string]interface{}{"request_id": id},
			}))
			if !strings.Contains(released, tc.marker) {
				t.Errorf("control failed: get_result after release has no %s, so its absence before release proved nothing: %s", tc.marker, released)
			}
		})

		t.Run(tc.name+"/approve-gated during wait", func(t *testing.T) {
			decided := approveGatedWhenPending(s, upstream.URL+tc.path)
			resp := submit(tc.path, 10)
			if err := <-decided; err != nil {
				t.Fatalf("approve-gated: %v", err)
			}
			if isErrorResponse(resp) {
				t.Fatalf("wait returned an error: %s", toolText(t, resp))
			}
			text := toolText(t, resp)
			var m bodilessRead
			json.Unmarshal([]byte(text), &m)
			approverSees(t, m.ID, tc.marker)
			agentRead(t, "http_request with wait", text, tc.marker, tc.status, tc.code)
		})
	}

	t.Run("gate_output rule during wait", func(t *testing.T) {
		resp := submit("/no-content-rule", 10)
		if isErrorResponse(resp) {
			t.Fatalf("wait returned an error: %s", toolText(t, resp))
		}
		text := toolText(t, resp)
		var m bodilessRead
		json.Unmarshal([]byte(text), &m)
		approverSees(t, m.ID, bodilessCookieMarker)
		agentRead(t, "http_request with wait", text, bodilessCookieMarker, "complete", http.StatusNoContent)
		agentRead(t, "agent GET /api/requests", webEntry(t, agentToken, m.ID), bodilessCookieMarker, "complete", http.StatusNoContent)
	})
}

// bodilessRead is the part of an agent-visible request this test checks.
type bodilessRead struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	OutputGated bool   `json:"output_gated"`
	Result      *struct {
		StatusCode int `json:"status_code"`
	} `json:"result"`
}

// approveGatedWhenPending approves, as the approver and with output gated, the
// pending http_request for url once it appears. It reports through the
// returned channel instead of t, which must not be used off the test
// goroutine.
func approveGatedWhenPending(s *TestServer, url string) <-chan error {
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			req, _ := http.NewRequest(http.MethodGet, s.WebURL()+"/api/requests?status=pending", nil)
			req.Header.Set("Authorization", "Bearer "+approverToken)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				var list []struct {
					ID      string `json:"id"`
					HTTPURL string `json:"http_url"`
				}
				json.NewDecoder(resp.Body).Decode(&list)
				resp.Body.Close()
				for _, r := range list {
					if r.HTTPURL != url {
						continue
					}
					post, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/requests/%s/approve-gated", s.WebURL(), r.ID), nil)
					post.Header.Set("Authorization", "Bearer "+approverToken)
					presp, err := http.DefaultClient.Do(post)
					if err != nil {
						done <- err
						return
					}
					presp.Body.Close()
					if presp.StatusCode != http.StatusOK {
						done <- fmt.Errorf("approve-gated %s returned %d", r.ID, presp.StatusCode)
						return
					}
					done <- nil
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		done <- fmt.Errorf("no pending request for %s appeared", url)
	}()
	return done
}
