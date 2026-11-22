package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/integration"
	"github.com/standardnguyen/human-relay/tui/api"
	"github.com/standardnguyen/human-relay/tui/internal/testrelay"
)

func approverClient(t *testing.T, r *testrelay.Relay) *api.Client {
	t.Helper()
	c, err := api.New(r.URL(), []byte(testrelay.ApproverToken))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func find(l *api.Listing, id string) *api.Request {
	for i := range l.Requests {
		if l.Requests[i].ID == id {
			return &l.Requests[i]
		}
	}
	return nil
}

func waitStatus(t *testing.T, c *api.Client, id, want string) *api.Request {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		l, err := c.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if q := find(l, id); q != nil && q.Status == want {
			return q
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("request %s never reached %s", id, want)
	return nil
}

func TestRelayListApproveAndResult(t *testing.T) {
	r := testrelay.WithApprover(t)
	c := approverClient(t, r)
	ctx := context.Background()
	id := r.Command(t, "echo", []string{"hello", "tui"}, "say hello")

	l, err := c.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	q := find(l, id)
	if q == nil || q.Status != "pending" || q.Command != "echo" || len(q.Args) != 2 || q.Reason != "say hello" {
		t.Fatalf("listed request = %+v", q)
	}
	if q.Client == "" || q.CreatedAt.IsZero() {
		t.Fatalf("client/created_at not parsed: %+v", q)
	}
	if err := c.Approve(ctx, id); err != nil {
		t.Fatal(err)
	}
	done := waitStatus(t, c, id, "complete")
	if done.Result == nil || !strings.Contains(done.Result.Stdout, "hello tui") {
		t.Fatalf("result = %+v", done.Result)
	}
	// Deciding twice is a typed conflict.
	if err := c.Approve(ctx, id); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("second approve = %v", err)
	}
}

func TestRelayAgentTokenCannotDecide(t *testing.T) {
	r := testrelay.WithApprover(t)
	id := r.Command(t, "echo", []string{"x"}, "r")
	agent, _ := api.New(r.URL(), []byte(testrelay.AgentToken))
	err := agent.Approve(context.Background(), id)
	var e *api.Error
	if !errors.Is(err, api.ErrForbidden) || !errors.As(err, &e) || !e.NotApprover() {
		t.Fatalf("err = %v", err)
	}
	// An agent token may still read the list.
	if _, err := agent.List(context.Background()); err != nil {
		t.Fatalf("agent list: %v", err)
	}
}

func TestRelayBadTokenIsUnauthorized(t *testing.T) {
	r := testrelay.WithApprover(t)
	bad, _ := api.New(r.URL(), []byte("nope"))
	if _, err := bad.List(context.Background()); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
}

func TestRelayProbeApproverAgentAndLegacy(t *testing.T) {
	r := testrelay.WithApprover(t)
	if err := approverClient(t, r).Probe(context.Background()); err != nil {
		t.Fatalf("approver probe: %v", err)
	}
	agent, _ := api.New(r.URL(), []byte(testrelay.AgentToken))
	if err := agent.Probe(context.Background()); !errors.Is(err, api.ErrForbidden) {
		t.Fatalf("agent probe: %v", err)
	}
	bad, _ := api.New(r.URL(), []byte("zzz"))
	if err := bad.Probe(context.Background()); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("bad probe: %v", err)
	}
	// The probe changed nothing: no request, no cooldown.
	l, _ := approverClient(t, r).List(context.Background())
	if len(l.Requests) != 0 || l.CooldownRemaining != 0 {
		t.Fatalf("probe had side effects: %+v", l)
	}

	legacy := testrelay.Start(t)
	lc, _ := api.New(legacy.URL(), []byte(testrelay.AgentToken))
	if err := lc.Probe(context.Background()); err != nil {
		t.Fatalf("legacy probe: %v", err)
	}
}

func TestRelayCooldown429AndHeaders(t *testing.T) {
	r := testrelay.WithApprover(t, integration.WithCooldown(30))
	c := approverClient(t, r)
	ctx := context.Background()
	a := r.Command(t, "echo", []string{"a"}, "first")
	b := r.Command(t, "echo", []string{"b"}, "second")
	if err := c.Approve(ctx, a); err != nil {
		t.Fatal(err)
	}
	err := c.Approve(ctx, b)
	var e *api.Error
	if !errors.Is(err, api.ErrCooldown) || !errors.As(err, &e) {
		t.Fatalf("second approve = %v", err)
	}
	if e.RemainingMs <= 0 || e.RemainingMs > 30000 || e.RetryAfter < time.Second || e.RetryAfter > 31*time.Second {
		t.Fatalf("429 detail remaining=%d retry=%v", e.RemainingMs, e.RetryAfter)
	}
	// Gated approve is subject to the same cooldown; deny is not.
	if err := c.ApproveGated(ctx, b); !errors.Is(err, api.ErrCooldown) {
		t.Fatalf("gated = %v", err)
	}
	l, _ := c.List(ctx)
	if l.CooldownDuration != 30*time.Second || l.CooldownRemaining <= 0 || l.CooldownRemaining > 30*time.Second {
		t.Fatalf("cooldown headers = %v / %v", l.CooldownRemaining, l.CooldownDuration)
	}
	if err := c.Deny(ctx, b, "later"); err != nil {
		t.Fatalf("deny during cooldown: %v", err)
	}
}

func TestRelayDenyReasonSemantics(t *testing.T) {
	r := testrelay.WithApprover(t)
	c := approverClient(t, r)
	ctx := context.Background()
	a := r.Command(t, "echo", []string{"a"}, "x")
	b := r.Command(t, "echo", []string{"b"}, "x")
	if err := c.Deny(ctx, a, "too risky"); err != nil {
		t.Fatal(err)
	}
	if err := c.Deny(ctx, b, ""); err != nil {
		t.Fatal(err)
	}
	l, _ := c.List(ctx)
	if q := find(l, a); q.Status != "denied" || q.DenyReason != "too risky" {
		t.Fatalf("a = %+v", q)
	}
	if q := find(l, b); q.DenyReason != "denied by operator" {
		t.Fatalf("empty reason should take the relay default, got %q", q.DenyReason)
	}
}

func TestRelayGatedOutputAndRelease(t *testing.T) {
	r := testrelay.WithApprover(t)
	c := approverClient(t, r)
	ctx := context.Background()
	id := r.Command(t, "echo", []string{"secret-output"}, "gated")
	if err := c.ApproveGated(ctx, id); err != nil {
		t.Fatal(err)
	}
	q := waitStatus(t, c, id, "complete")
	if !q.OutputGated {
		t.Fatalf("not gated: %+v", q)
	}
	// The approver reads the true output; an agent token sees the placeholder.
	if !strings.Contains(q.Result.Stdout, "secret-output") {
		t.Fatalf("approver should see output: %q", q.Result.Stdout)
	}
	agent, _ := api.New(r.URL(), []byte(testrelay.AgentToken))
	al, _ := agent.List(ctx)
	if aq := find(al, id); strings.Contains(aq.Result.Stdout, "secret-output") {
		t.Fatalf("agent view leaked output: %q", aq.Result.Stdout)
	}
	if err := c.Release(ctx, id); err != nil {
		t.Fatal(err)
	}
	l, _ := c.List(ctx)
	if find(l, id).OutputGated {
		t.Fatal("still gated after release")
	}
	if err := c.Release(ctx, id); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("release of an ungated request = %v", err)
	}
}

func TestRelayWhitelistAddListRemove(t *testing.T) {
	r := testrelay.WithApprover(t)
	c := approverClient(t, r)
	ctx := context.Background()
	id := r.Command(t, "echo", []string{"wl", "me"}, "x")
	if err := c.Approve(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, id, "complete")
	if err := c.AddWhitelist(ctx, id); err != nil {
		t.Fatal(err)
	}
	rules, err := c.Whitelist(ctx)
	if err != nil || len(rules) != 1 || rules[0].Command != "echo" || strings.Join(rules[0].Args, " ") != "wl me" {
		t.Fatalf("rules = %+v err %v", rules, err)
	}
	if err := c.AddWhitelist(ctx, "ffffffffffffffff"); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("unknown request id = %v", err)
	}
	if err := c.RemoveWhitelist(ctx, rules[0]); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveWhitelist(ctx, rules[0]); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("second remove = %v", err)
	}
	rules, _ = c.Whitelist(ctx)
	if len(rules) != 0 {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestRelayWhitelistedCommandIsAutoApproved(t *testing.T) {
	r := testrelay.WithApprover(t)
	c := approverClient(t, r)
	ctx := context.Background()
	id := r.Command(t, "echo", []string{"auto"}, "x")
	if err := c.Approve(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, id, "complete")
	if err := c.AddWhitelist(ctx, id); err != nil {
		t.Fatal(err)
	}
	id2 := r.Command(t, "echo", []string{"auto"}, "again")
	waitStatus(t, c, id2, "complete")
}

func TestRelayTurboOnOff(t *testing.T) {
	r := testrelay.WithApprover(t, integration.WithCooldown(30))
	c := approverClient(t, r)
	ctx := context.Background()
	if err := c.TurboOn(ctx, 5, 10); err != nil {
		t.Fatal(err)
	}
	l, _ := c.List(ctx)
	if !l.TurboActive || l.TurboRemaining <= 4*time.Minute || l.TurboRemaining > 5*time.Minute {
		t.Fatalf("turbo = %v %v", l.TurboActive, l.TurboRemaining)
	}
	if l.CooldownDuration != 10*time.Second {
		t.Fatalf("active cooldown = %v, want the turbo cooldown", l.CooldownDuration)
	}
	ts, err := c.Turbo(ctx)
	if err != nil || !ts.Active || ts.Cooldown != 10*time.Second {
		t.Fatalf("turbo state = %+v %v", ts, err)
	}
	if err := c.TurboOff(ctx); err != nil {
		t.Fatal(err)
	}
	l, _ = c.List(ctx)
	if l.TurboActive || l.CooldownDuration != 30*time.Second {
		t.Fatalf("after off: %v %v", l.TurboActive, l.CooldownDuration)
	}
	agent, _ := api.New(r.URL(), []byte(testrelay.AgentToken))
	if err := agent.TurboOn(ctx, 5, 10); !errors.Is(err, api.ErrForbidden) {
		t.Fatalf("agent turbo = %v", err)
	}
}

func TestRelayContentAndHash(t *testing.T) {
	r := testrelay.WithApprover(t)
	c := approverClient(t, r)
	body := "line one\nline two with \u0430 cyrillic\n"
	id := r.WriteFile(t, "/tmp/tui-test.txt", body, "write it")
	l, _ := c.List(context.Background())
	q := find(l, id)
	if q == nil || q.StdinLen != len(body) || q.StdinSHA256 == "" {
		t.Fatalf("request = %+v", q)
	}
	got, err := c.Content(context.Background(), id)
	if err != nil || string(got.Body) != body {
		t.Fatalf("content = %q err %v", got.Body, err)
	}
	sum := sha256.Sum256(got.Body)
	if got.SHA256 != hex.EncodeToString(sum[:]) || got.SHA256 != q.StdinSHA256 {
		t.Fatalf("hash header %q, computed %x, listed %q", got.SHA256, sum, q.StdinSHA256)
	}
	if _, err := c.Content(context.Background(), "ffffffffffffffff"); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("content of unknown id = %v", err)
	}
}

func TestRelayHTTPRequestFields(t *testing.T) {
	r := testrelay.WithApprover(t)
	c := approverClient(t, r)
	id := r.HTTP(t, "POST", "http://127.0.0.1:9/x", map[string]string{"Authorization": "Bearer abcdefghijklmnop", "X-A": "b"}, `{"k":1}`, "call")
	l, _ := c.List(context.Background())
	q := find(l, id)
	if q.Type != "http" || q.HTTPMethod != "POST" || q.HTTPURL != "http://127.0.0.1:9/x" || q.HTTPBody != `{"k":1}` || q.HTTPHeaders["X-A"] != "b" {
		t.Fatalf("http request = %+v", q)
	}
}

func TestRelayEventsFramesOnNewAndUpdate(t *testing.T) {
	r := testrelay.WithApprover(t)
	c := approverClient(t, r)
	var mu sync.Mutex
	var frames []api.Frame
	opened := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Events(ctx, func() { opened <- struct{}{} }, func(f api.Frame) { mu.Lock(); frames = append(frames, f); mu.Unlock() })
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("event stream did not open")
	}
	id := r.Command(t, "echo", []string{"e"}, "x")
	if err := c.Deny(context.Background(), id, "no"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		var sawNew, sawUpdate bool
		for _, f := range frames {
			if f.RequestID == id && f.Type == "new" {
				sawNew = true
			}
			if f.RequestID == id && f.Type == "update" && f.Status == "denied" {
				sawUpdate = true
			}
		}
		mu.Unlock()
		if sawNew && sawUpdate {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("frames = %+v", frames)
}
