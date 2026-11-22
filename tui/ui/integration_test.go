package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/integration"
	"github.com/standardnguyen/human-relay/tui/api"
	"github.com/standardnguyen/human-relay/tui/internal/testrelay"
)

// harness runs the real state machine against a real relay: effects go to the
// relay through runEffect, their answers come back as events.
type harness struct {
	t      *testing.T
	m      Model
	c      *api.Client
	events chan Event
	ctx    context.Context
}

func newHarness(t *testing.T, r *testrelay.Relay, token string) *harness {
	t.Helper()
	c, err := api.New(r.URL(), []byte(token))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{t: t, c: c, events: make(chan Event, 256), ctx: ctx}
	h.m = NewModel(Options{URL: r.URL(), Bell: false, Loc: time.UTC})
	h.m.Width, h.m.Height = 120, 40
	go c.Watch(ctx, api.Backoff{Min: 20 * time.Millisecond, Max: 100 * time.Millisecond},
		func(up bool) { h.post(ConnEvent{Up: up}) }, func(f api.Frame) { h.post(FrameEvent{Frame: f}) })
	h.apply(TickEvent{Now: time.Now()})
	var effs []Effect
	h.m, effs = Init(h.m)
	h.run(effs)
	return h
}

func (h *harness) post(e Event) {
	select {
	case h.events <- e:
	case <-h.ctx.Done():
	}
}

func (h *harness) run(effs []Effect) {
	for _, e := range effs {
		switch e.(type) {
		case Quit, RingBell:
		default:
			runEffect(h.ctx, h.c, e, h.post, func(f func()) { go f() })
		}
	}
}

func (h *harness) apply(ev Event) {
	h.m.Now = time.Now()
	var effs []Effect
	h.m, effs = Update(h.m, ev)
	h.run(effs)
}

func (h *harness) keys(ks ...string) {
	for _, k := range ks {
		h.apply(KeyEvent{Key: keyOf(k)})
	}
}

func keyOf(s string) Key {
	switch s {
	case "enter":
		return Key{Type: KeyEnter}
	case "esc":
		return Key{Type: KeyEsc}
	}
	return Key{Type: KeyRune, Rune: []rune(s)[0]}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.apply(KeyEvent{Key: Key{Type: KeyRune, Rune: r}})
	}
}

// until applies incoming events until cond holds.
func (h *harness) until(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case ev := <-h.events:
			h.apply(ev)
		case <-tick.C:
			h.apply(TickEvent{Now: time.Now()})
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s\nstatus: %q\nscreen:\n%s", what, h.m.Status.Text, Render(h.m, Theme{}))
		}
	}
}

func (h *harness) reqStatus(id string) string {
	for _, r := range h.m.Requests {
		if r.ID == id {
			return r.Status
		}
	}
	return ""
}

func (h *harness) screen() string { return Render(h.m, Theme{}) }

func (h *harness) selectID(id string) {
	h.t.Helper()
	h.until("request "+id+" listed", func() bool { _, ok := h.m.byID(id); return ok })
	h.apply(KeyEvent{Key: Key{Type: KeyHome}})
	for i := 0; i < 50 && h.m.Sel != id; i++ {
		h.keys("j")
	}
	if h.m.Sel != id {
		h.t.Fatalf("cannot select %s", id)
	}
}

func TestRelayApproveFlowThroughTheStateMachine(t *testing.T) {
	r := testrelay.WithApprover(t)
	h := newHarness(t, r, testrelay.ApproverToken)
	id := r.Command(t, "echo", []string{"from", "the", "tui"}, "say hi")
	h.until("connected", func() bool { return h.m.Conn == ConnUp })
	h.selectID(id)
	// The cooldown guard from an auto-moved selection does not apply: the
	// selection did not move on its own.
	h.keys("enter", "a")
	h.until("complete", func() bool { return h.reqStatus(id) == api.StatusComplete })
	scr := h.screen() // still the open request, now with its result
	if !strings.Contains(scr, "stdout (exit 0)") || !strings.Contains(scr, "from the tui") {
		t.Fatalf("result not shown:\n%s", scr)
	}
	if !strings.Contains(h.m.Status.Text, "approved") {
		t.Fatalf("status = %q", h.m.Status.Text)
	}
}

func TestRelayDenyWithReason(t *testing.T) {
	r := testrelay.WithApprover(t)
	h := newHarness(t, r, testrelay.ApproverToken)
	id := r.Command(t, "rm", []string{"-rf", "/"}, "oops")
	h.selectID(id)
	h.keys("d")
	h.typeText("absolutely not")
	h.keys("enter")
	h.until("denied", func() bool { return h.reqStatus(id) == api.StatusDenied })
	h.keys("enter")
	if scr := h.screen(); !strings.Contains(scr, "Denied: absolutely not") {
		t.Fatalf("deny reason not shown:\n%s", scr)
	}
}

func TestRelayGatedApproveThenRelease(t *testing.T) {
	r := testrelay.WithApprover(t)
	h := newHarness(t, r, testrelay.ApproverToken)
	id := r.Command(t, "echo", []string{"held-back"}, "gated")
	h.selectID(id)
	h.keys("enter", "g")
	h.until("complete", func() bool { return h.reqStatus(id) == api.StatusComplete })
	if !strings.Contains(h.screen(), "GATED") {
		t.Fatalf("no GATED badge:\n%s", h.screen())
	}
	h.keys("r")
	h.until("released", func() bool {
		for _, q := range h.m.Requests {
			if q.ID == id {
				return !q.OutputGated
			}
		}
		return false
	})
}

func TestRelayApproveAndWhitelistThenRemove(t *testing.T) {
	r := testrelay.WithApprover(t)
	h := newHarness(t, r, testrelay.ApproverToken)
	id := r.Command(t, "echo", []string{"wl-flow"}, "x")
	h.selectID(id)
	h.keys("enter", "A")
	if h.m.Prompt == nil || h.m.Prompt.Kind != PromptApproveWhitelist {
		t.Fatalf("no confirm: %+v", h.m.Prompt)
	}
	h.keys("y")
	// The rule list and the request list are fetched separately and may be
	// applied in either order, so wait for both.
	h.until("rule listed and request decided", func() bool {
		return len(h.m.Rules) == 1 && h.reqStatus(id) != api.StatusPending
	})
	if h.m.Rules[0].Command != "echo" || h.reqStatus(id) == api.StatusPending {
		t.Fatalf("rules %+v status %s", h.m.Rules, h.reqStatus(id))
	}
	h.keys("esc", "9")
	if scr := h.screen(); !strings.Contains(scr, "echo wl-flow") {
		t.Fatalf("whitelist view:\n%s", scr)
	}
	h.keys("x", "y")
	h.until("rule removed", func() bool { return len(h.m.Rules) == 0 })
	if scr := h.screen(); !strings.Contains(scr, "No whitelisted commands.") {
		t.Fatalf("whitelist view after removal:\n%s", scr)
	}
}

func TestRelayCooldown429ShownAndCountdown(t *testing.T) {
	r := testrelay.WithApprover(t, integration.WithCooldown(30))
	h := newHarness(t, r, testrelay.ApproverToken)
	a := r.Command(t, "echo", []string{"one"}, "x")
	b := r.Command(t, "echo", []string{"two"}, "x")
	h.selectID(a)
	h.keys("enter", "a")
	h.until("cooldown known", func() bool { return h.m.CooldownLeft() > 20*time.Second })
	if !strings.Contains(h.screen(), "cooldown") {
		t.Fatalf("header should show the cooldown:\n%s", h.screen())
	}
	h.keys("esc")
	h.selectID(b)
	time.Sleep(selectionGuard + 100*time.Millisecond) // the selection moved on its own after the approve
	h.keys("enter", "a")
	h.until("429 shown", func() bool { return strings.Contains(h.m.Status.Text, "cooldown active") })
	if h.reqStatus(b) != api.StatusPending {
		t.Fatalf("b should still be pending, is %s", h.reqStatus(b))
	}
}

func TestRelayNotApproverTokenOffersReentry(t *testing.T) {
	r := testrelay.WithApprover(t)
	h := newHarness(t, r, testrelay.AgentToken)
	id := r.Command(t, "echo", []string{"x"}, "x")
	h.selectID(id)
	h.keys("enter", "a")
	h.until("reauth prompt", func() bool { return h.m.Prompt != nil && h.m.Prompt.Kind == PromptReauth })
	h.keys("y")
	if h.m.Prompt == nil || h.m.Prompt.Kind != PromptToken || h.m.HaveToken {
		t.Fatalf("prompt %+v have %v", h.m.Prompt, h.m.HaveToken)
	}
	h.typeText(testrelay.ApproverToken)
	h.keys("enter")
	h.until("token accepted", func() bool { return h.m.HaveToken })
	h.until("requests reloaded", func() bool { return h.reqStatus(id) == api.StatusPending })
	h.keys("enter", "a")
	h.until("approved", func() bool { return h.reqStatus(id) != api.StatusPending })
}

func TestRelayTurboToggle(t *testing.T) {
	r := testrelay.WithApprover(t)
	h := newHarness(t, r, testrelay.ApproverToken)
	h.keys("t")
	h.until("turbo on", func() bool { return h.m.TurboActive })
	if !strings.Contains(h.screen(), "Turbo ON 5:00") && !strings.Contains(h.screen(), "Turbo ON 4:5") {
		t.Fatalf("header should show the countdown:\n%s", h.screen())
	}
	h.keys("t")
	h.until("turbo off", func() bool { return !h.m.TurboActive })
}

func TestRelayFullFileContent(t *testing.T) {
	r := testrelay.WithApprover(t)
	h := newHarness(t, r, testrelay.ApproverToken)
	body := "alpha\nbeta with p\u0430ss\n"
	id := r.WriteFile(t, "/tmp/x.conf", body, "deploy")
	h.selectID(id)
	h.keys("enter", "f")
	h.until("content loaded", func() bool { st := h.m.Content[id]; return st != nil && st.State == ContentReady })
	scr := h.screen()
	for _, want := range []string{"hide full file", "alpha", "non-ASCII in content: U+0430 (Cyrillic)", "FILE"} {
		if !strings.Contains(scr, want) {
			t.Errorf("missing %q in:\n%s", want, scr)
		}
	}
	if strings.Contains(scr, "differs") {
		t.Fatalf("hash of fetched content should match the listing:\n%s", scr)
	}
}

func TestRelayHostileRequestRendersSafely(t *testing.T) {
	r := testrelay.WithApprover(t)
	h := newHarness(t, r, testrelay.ApproverToken)
	esc := "\x1b[2J\x1b]52;c;aGVsbG8=\x07\x1b]0;pwned\x07\u202e"
	id := r.Command(t, "echo"+esc, []string{esc, "p\u0430ss"}, "reason "+esc)
	h.selectID(id)
	for _, th := range []Theme{{}, {Color: true, Attrs: true}} {
		h.m.Mode = ModeList
		assertCleanBytes(t, "list", Render(h.m, th), th)
		h.keys("enter")
		assertCleanBytes(t, "detail", Render(h.m, th), th)
		h.keys("esc")
	}
	if !strings.Contains(strings.Join(strings.Fields(h.screen()), " "), `\x1b[2J`) {
		t.Fatalf("escape should be shown as text:\n%s", h.screen())
	}
}
