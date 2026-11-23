package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/tui/api"
)

const tok = "mhra_unit_token_value"

type seen struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (s *seen) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, r)
	s.body = append(s.body, string(b))
}

func (s *seen) last() (*http.Request, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[len(s.reqs)-1], s.body[len(s.body)-1]
}

func newFake(t *testing.T, h http.HandlerFunc) (*api.Client, *seen) {
	t.Helper()
	sn := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sn.record(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := api.New(srv.URL, []byte(tok))
	if err != nil {
		t.Fatal(err)
	}
	return c, sn
}

func TestListParsesRequestsAndHeaders(t *testing.T) {
	c, sn := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Cooldown-Remaining-Ms", "1500")
		w.Header().Set("X-Cooldown-Duration-Ms", "30000")
		w.Header().Set("X-Turbo-Active", "true")
		w.Header().Set("X-Turbo-Remaining-Ms", "120000")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"id":"aaaa","type":"","command":"ls","args":null,"reason":"r","status":"pending","created_at":"2026-11-21T10:00:00Z","future_field":"\u00e9","stdin_len":12},
{"id":"bbbb","type":"http","command":"","args":null,"http_method":"POST","http_url":"https://x/y","http_headers":{"A":"b"},"status":"complete","created_at":"2026-11-21T10:01:00Z","result":{"exit_code":0,"stdout":"hi","stderr":"","status_code":200,"response_headers":{"K":"V"}}}]`)
	})
	got, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Requests) != 2 || got.Requests[0].ID != "aaaa" || got.Requests[0].Args != nil {
		t.Fatalf("requests = %+v", got.Requests)
	}
	if got.Requests[0].StdinLen != 12 || got.Requests[1].Result == nil || got.Requests[1].Result.StatusCode != 200 {
		t.Fatalf("fields not parsed: %+v", got.Requests)
	}
	if got.CooldownRemaining != 1500*time.Millisecond || got.CooldownDuration != 30*time.Second {
		t.Fatalf("cooldown = %v / %v", got.CooldownRemaining, got.CooldownDuration)
	}
	if !got.TurboActive || got.TurboRemaining != 2*time.Minute {
		t.Fatalf("turbo = %v %v", got.TurboActive, got.TurboRemaining)
	}
	// The raw JSON of every request is kept so unknown fields can be inspected.
	if !strings.Contains(string(got.Requests[0].Raw), "future_field") {
		t.Fatalf("raw lost: %s", got.Requests[0].Raw)
	}
	r, _ := sn.last()
	if r.Header.Get("Authorization") != "Bearer "+tok {
		t.Fatalf("auth header = %q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("Origin") != "" {
		t.Fatalf("Origin must never be sent, got %q", r.Header.Get("Origin"))
	}
	if r.URL.Path != "/api/requests" || r.URL.RawQuery != "" {
		t.Fatalf("url = %s", r.URL)
	}
}

func TestListWithoutTurboHeaders(t *testing.T) {
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `[]`) })
	got, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.TurboActive || got.CooldownRemaining != 0 || got.CooldownDuration != 0 || len(got.Requests) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestListRejectsNonArray(t *testing.T) {
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"x":1}`) })
	if _, err := c.List(context.Background()); err == nil {
		t.Fatal("want a decode error")
	}
}

func TestTypedErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		retry  string
		target error
	}{
		{"401", 401, "unauthorized", "", api.ErrUnauthorized},
		{"403", 403, "forbidden: this token cannot approve. more", "", api.ErrForbidden},
		{"404", 404, "request not found", "", api.ErrNotFound},
		{"409", 409, "request is already denied", "", api.ErrConflict},
		{"429", 429, `{"error":"cooldown active","remaining_ms":4200}`, "5", api.ErrCooldown},
		{"500", 500, "boom", "", api.ErrServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.retry != "" {
					w.Header().Set("Retry-After", tc.retry)
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			err := c.Approve(context.Background(), "abc")
			if !errors.Is(err, tc.target) {
				t.Fatalf("err = %v, want %v", err, tc.target)
			}
			var e *api.Error
			if !errors.As(err, &e) || e.Status != tc.status {
				t.Fatalf("not an *api.Error with status %d: %v", tc.status, err)
			}
			if strings.Contains(err.Error(), tok) {
				t.Fatal("token leaked into error text")
			}
			if tc.status == 429 {
				if e.RemainingMs != 4200 || e.RetryAfter != 5*time.Second {
					t.Fatalf("429 detail = %d / %v", e.RemainingMs, e.RetryAfter)
				}
				if e.Remaining() != 4200*time.Millisecond {
					t.Fatalf("Remaining() = %v", e.Remaining())
				}
			}
			if tc.status == 403 && !e.NotApprover() {
				t.Fatal("403 'cannot approve' must report NotApprover")
			}
		})
	}
}

func TestForbiddenWithoutApproverTextIsNotNotApprover(t *testing.T) {
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "CSRF validation failed", 403)
	})
	err := c.Approve(context.Background(), "abc")
	var e *api.Error
	if !errors.As(err, &e) || e.NotApprover() {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryAfterOnlyWhenBodyHasNoRemaining(t *testing.T) {
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		io.WriteString(w, "slow down")
	})
	var e *api.Error
	if err := c.ApproveGated(context.Background(), "x"); !errors.As(err, &e) || e.Remaining() != 7*time.Second {
		t.Fatalf("err = %v, remaining %v", err, e)
	}
}

func TestDecisionRoutesAndBodies(t *testing.T) {
	c, sn := newFake(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"ok"}`) })
	ctx := context.Background()
	cases := []struct {
		do     func() error
		method string
		path   string
		body   string
	}{
		{func() error { return c.Approve(ctx, "id1") }, "POST", "/api/requests/id1/approve", ""},
		{func() error { return c.ApproveGated(ctx, "id2") }, "POST", "/api/requests/id2/approve-gated", ""},
		{func() error { return c.Release(ctx, "id3") }, "POST", "/api/requests/id3/release", ""},
		{func() error { return c.Deny(ctx, "id4", "no way") }, "POST", "/api/requests/id4/deny", `{"reason":"no way"}`},
		{func() error { return c.Deny(ctx, "id5", "") }, "POST", "/api/requests/id5/deny", `{"reason":""}`},
		{func() error { return c.AddWhitelist(ctx, "id6") }, "POST", "/api/whitelist", `{"request_id":"id6"}`},
		{func() error { return c.RemoveWhitelist(ctx, api.Rule{Command: "ls", Args: []string{"-l"}}) }, "POST", "/api/whitelist/remove", `{"command":"ls","args":["-l"]}`},
		{func() error { return c.RemoveWhitelist(ctx, api.Rule{Command: "ls"}) }, "POST", "/api/whitelist/remove", `{"command":"ls","args":[]}`},
		{func() error { return c.TurboOn(ctx, 5, 10) }, "POST", "/api/turbocharge", `{"duration_minutes":5,"cooldown_seconds":10}`},
		{func() error { return c.TurboOff(ctx) }, "DELETE", "/api/turbocharge", ""},
	}
	for _, tc := range cases {
		if err := tc.do(); err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		r, body := sn.last()
		if r.Method != tc.method || r.URL.Path != tc.path || body != tc.body {
			t.Fatalf("got %s %s %q, want %s %s %q", r.Method, r.URL.Path, body, tc.method, tc.path, tc.body)
		}
		if r.Header.Get("Authorization") != "Bearer "+tok {
			t.Fatalf("%s: auth header missing", tc.path)
		}
		if r.Header.Get("Origin") != "" {
			t.Fatalf("%s: Origin set", tc.path)
		}
		if tc.body != "" && r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("%s: body without Content-Type json (%q)", tc.path, r.Header.Get("Content-Type"))
		}
		if strings.Contains(body, "gate_output") {
			t.Fatal("gate_output must never be sent")
		}
	}
}

func TestWhitelistGetNullArgs(t *testing.T) {
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"command":"ls","args":null},{"command":"echo","args":["a"]}]`)
	})
	rules, err := c.Whitelist(context.Background())
	if err != nil || len(rules) != 2 || len(rules[0].Args) != 0 || rules[1].Args[0] != "a" {
		t.Fatalf("rules = %+v err %v", rules, err)
	}
}

func TestContentReturnsBytesAndHash(t *testing.T) {
	raw := []byte("line1\n\x9b31m\xff\n")
	c, sn := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Stdin-SHA256", "deadbeef")
		w.Write(raw)
	})
	got, err := c.Content(context.Background(), "abc")
	if err != nil || string(got.Body) != string(raw) || got.SHA256 != "deadbeef" {
		t.Fatalf("got %+v err %v", got, err)
	}
	r, _ := sn.last()
	if r.URL.Path != "/api/requests/abc/content" || r.Method != "GET" {
		t.Fatalf("%s %s", r.Method, r.URL.Path)
	}
}

func TestContentRejectsUnsafeID(t *testing.T) {
	c, sn := newFake(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, id := range []string{"", "a/b", "../x", "a b", "a?b", "a\x1bb"} {
		if _, err := c.Content(context.Background(), id); err == nil {
			t.Fatalf("id %q accepted", id)
		}
		if err := c.Approve(context.Background(), id); err == nil {
			t.Fatalf("approve id %q accepted", id)
		}
	}
	if len(sn.reqs) != 0 {
		t.Fatal("a request went out for an unsafe id")
	}
}

func TestTurboGet(t *testing.T) {
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"active":true,"remaining_ms":90000,"cooldown_seconds":10}`)
	})
	ts, err := c.Turbo(context.Background())
	if err != nil || !ts.Active || ts.Remaining != 90*time.Second || ts.Cooldown != 10*time.Second {
		t.Fatalf("ts = %+v err %v", ts, err)
	}
}

func TestProbeUsesNonexistentApprove(t *testing.T) {
	var path string
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		http.Error(w, "request not found", 404)
	})
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("404 means accepted, got %v", err)
	}
	if !strings.HasPrefix(path, "/api/requests/") || !strings.HasSuffix(path, "/approve") {
		t.Fatalf("path = %s", path)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/requests/"), "/approve")
	if len(id) != 32 {
		t.Fatalf("probe id %q should be 32 random hex chars", id)
	}
}

func TestProbeForbiddenAndUnauthorized(t *testing.T) {
	for status, target := range map[int]error{403: api.ErrForbidden, 401: api.ErrUnauthorized} {
		c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden: this token cannot approve.", status)
		})
		if err := c.Probe(context.Background()); !errors.Is(err, target) {
			t.Fatalf("status %d: err = %v", status, err)
		}
	}
}

func TestRedirectIsRefused(t *testing.T) {
	var hit int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit++ }))
	defer other.Close()
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	})
	err := c.Approve(context.Background(), "abc")
	if err == nil {
		t.Fatal("redirect followed")
	}
	if hit != 0 {
		t.Fatal("the redirect target received a request")
	}
	if strings.Contains(err.Error(), tok) {
		t.Fatal("token in error")
	}
}

func TestProxyEnvironmentIsIgnored(t *testing.T) {
	var proxied int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied++ }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	c, _ := newFake(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "[]") })
	// Loopback is never proxied by Go, so also assert on the transport itself.
	if _, err := c.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if proxied != 0 {
		t.Fatal("request went through the environment proxy")
	}
	tr := api.TransportForTest(c)
	if tr.Proxy != nil {
		t.Fatal("transport must not consult proxy environment variables")
	}
}

func TestConnectionErrorDoesNotLeakToken(t *testing.T) {
	c, err := api.New("http://127.0.0.1:1", []byte(tok))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.List(context.Background())
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("err = %v", err)
	}
	var e *api.Error
	if errors.As(err, &e) {
		t.Fatalf("a transport failure is not an HTTP status error: %v", err)
	}
	if !errors.Is(err, api.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
}

func TestWipeZeroesTheCallersSlice(t *testing.T) {
	buf := []byte(tok)
	c, err := api.New("http://127.0.0.1:1", buf)
	if err != nil {
		t.Fatal(err)
	}
	c.Wipe()
	for _, b := range buf {
		if b != 0 {
			t.Fatal("token bytes not zeroed")
		}
	}
	if err := c.Approve(context.Background(), "abc"); !errors.Is(err, api.ErrNoToken) {
		t.Fatalf("after Wipe, calls must refuse: %v", err)
	}
}

func TestSetTokenReplacesAndZeroesOld(t *testing.T) {
	old := []byte("old-token")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		io.WriteString(w, "[]")
	}))
	defer srv.Close()
	c, _ := api.New(srv.URL, old)
	c.SetToken([]byte("new-token"))
	if _, err := c.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer new-token" {
		t.Fatalf("auth = %q", got)
	}
	for _, b := range old {
		if b != 0 {
			t.Fatal("old token not zeroed")
		}
	}
}

func TestNewTrimsWhitespaceAndValidatesURL(t *testing.T) {
	if _, err := api.New("not a url", []byte("x")); err == nil {
		t.Fatal("bad url accepted")
	}
	if _, err := api.New("ftp://x", []byte("x")); err == nil {
		t.Fatal("non-http scheme accepted")
	}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		io.WriteString(w, "[]")
	}))
	defer srv.Close()
	c, err := api.New(srv.URL+"/", []byte("  spaced-token \n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer spaced-token" {
		t.Fatalf("auth = %q", got)
	}
}

func TestBaseURLPathPrefixIsKept(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		io.WriteString(w, "[]")
	}))
	defer srv.Close()
	c, _ := api.New(srv.URL+"/relay/", []byte("x"))
	c.List(context.Background())
	if path != "/relay/api/requests" {
		t.Fatalf("path = %s", path)
	}
}

// --- SSE ---

func sseServer(t *testing.T, serve func(w http.ResponseWriter, r *http.Request, n int)) (*api.Client, *int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		cur := n
		mu.Unlock()
		if r.URL.Path != "/events" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the event stream request must not carry an Authorization header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		serve(w, r, cur)
	}))
	t.Cleanup(srv.Close)
	c, _ := api.New(srv.URL, []byte(tok))
	return c, &n
}

func TestEventsParsesFramesAndSignalsOpen(t *testing.T) {
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		io.WriteString(w, ": connected\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: {\"type\":\"new\",\"request_id\":\"abc\",\"status\":\"pending\",\"client\":\"c1\",\"output_gated\":false}\n\n")
		io.WriteString(w, "data: {\"type\":\"turbo\",\"request_id\":\"on\"}\n\n")
		io.WriteString(w, "data: not json\n\n")
		io.WriteString(w, ": comment only\n\n")
		io.WriteString(w, "data: {\"type\":\"update\",\"request_id\":\"abc\",\"status\":\"approved\"}\r\n\r\n")
	})
	var frames []api.Frame
	opened := 0
	err := c.Events(context.Background(), func() { opened++ }, func(f api.Frame) { frames = append(frames, f) })
	if err == nil {
		t.Fatal("a stream that ends must return an error so the caller reconnects")
	}
	if opened != 1 {
		t.Fatalf("opened = %d", opened)
	}
	if len(frames) != 4 {
		t.Fatalf("frames = %+v", frames)
	}
	if frames[0].Type != "new" || frames[0].RequestID != "abc" || frames[0].Status != "pending" || frames[0].Client != "c1" {
		t.Fatalf("frame 0 = %+v", frames[0])
	}
	if frames[1].Type != "turbo" || frames[1].RequestID != "on" {
		t.Fatalf("frame 1 = %+v", frames[1])
	}
	if frames[2].Type != "" {
		t.Fatalf("unparseable frame should still be delivered, with empty type: %+v", frames[2])
	}
	if frames[3].Status != "approved" {
		t.Fatalf("CRLF framing: %+v", frames[3])
	}
}

func TestEventsHasNoReadTimeout(t *testing.T) {
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		io.WriteString(w, ": connected\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(1500 * time.Millisecond) // silent, like an idle relay
		io.WriteString(w, "data: {\"type\":\"update\",\"request_id\":\"z\"}\n\n")
	})
	got := make(chan api.Frame, 1)
	go c.Events(context.Background(), func() {}, func(f api.Frame) { got <- f })
	select {
	case f := <-got:
		if f.RequestID != "z" {
			t.Fatalf("frame = %+v", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("frame after a quiet period never arrived")
	}
}

func TestEventsContextCancelStopsIt(t *testing.T) {
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		io.WriteString(w, ": connected\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	opened := make(chan struct{})
	go func() { done <- c.Events(ctx, func() { close(opened) }, func(api.Frame) {}) }()
	<-opened
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Events did not return after cancel")
	}
}

func TestEventsNon200IsAnError(t *testing.T) {
	c, _ := sseServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		http.Error(w, "nope", 503)
	})
	err := c.Events(context.Background(), func() { t.Error("opened on 503") }, func(api.Frame) {})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestWatchReconnectsAndReportsState(t *testing.T) {
	c, count := sseServer(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			io.WriteString(w, ": connected\n\n")
			w.(http.Flusher).Flush()
			io.WriteString(w, "data: {\"type\":\"new\",\"request_id\":\"one\"}\n\n")
			return // drop the stream
		}
		if n == 2 {
			http.Error(w, "down", 503)
			return
		}
		io.WriteString(w, ": connected\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: {\"type\":\"new\",\"request_id\":\"three\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	var mu sync.Mutex
	var states []bool
	var ids []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Watch(ctx, api.Backoff{Min: 10 * time.Millisecond, Max: 40 * time.Millisecond},
		func(up bool) { mu.Lock(); states = append(states, up); mu.Unlock() },
		func(f api.Frame) { mu.Lock(); ids = append(ids, f.RequestID); mu.Unlock() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := len(ids) == 2
		mu.Unlock()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] != "one" || ids[1] != "three" {
		t.Fatalf("ids = %v (connections: %d)", ids, *count)
	}
	// up, down (stream dropped), then up again; the failed 503 attempt stays down.
	want := []bool{true, false, true}
	if len(states) < 3 {
		t.Fatalf("states = %v", states)
	}
	for i, w := range want {
		if states[i] != w {
			t.Fatalf("states = %v, want prefix %v", states, want)
		}
	}
}

func TestDecodeFrameFields(t *testing.T) {
	var f api.Frame
	if err := json.Unmarshal([]byte(`{"type":"update","request_id":"a","status":"x","client":"c","output_gated":true}`), &f); err != nil || !f.OutputGated {
		t.Fatalf("%+v %v", f, err)
	}
}
