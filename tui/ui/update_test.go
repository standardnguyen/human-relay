package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/tui/api"
)

var t0 = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

type opt func(*api.Request)

func mk(id, status string, minutes int, opts ...opt) api.Request {
	r := api.Request{
		ID: id, Status: status, Command: "cmd-" + id, Args: []string{"a1"}, Reason: "because",
		CreatedAt: t0.Add(time.Duration(minutes) * time.Minute),
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func gated(r *api.Request) { r.OutputGated = true }
func stdin(n int) opt      { return func(r *api.Request) { r.StdinLen = n } }
func registry(r *api.Request) {
	r.Type = api.TypeRegistryOp
	r.Command = ""
	r.RegistryOp = "register_container"
}

func newModel() Model {
	m := NewModel(Options{URL: "http://127.0.0.1:8090", Bell: true, Loc: time.UTC})
	m.Now = t0
	m.Width, m.Height = 100, 30
	return m
}

func listing(reqs ...api.Request) *api.Listing { return &api.Listing{Requests: reqs} }

var seq int

func loaded(m Model, reqs ...api.Request) Model {
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: listing(reqs...)})
	return m
}

func k(s string) Key {
	switch s {
	case "enter":
		return Key{Type: KeyEnter}
	case "esc":
		return Key{Type: KeyEsc}
	case "up":
		return Key{Type: KeyUp}
	case "down":
		return Key{Type: KeyDown}
	case "left":
		return Key{Type: KeyLeft}
	case "pgdn":
		return Key{Type: KeyPgDn}
	case "pgup":
		return Key{Type: KeyPgUp}
	case "home":
		return Key{Type: KeyHome}
	case "end":
		return Key{Type: KeyEnd}
	case "tab":
		return Key{Type: KeyTab}
	case "backtab":
		return Key{Type: KeyBacktab}
	case "backspace":
		return Key{Type: KeyBackspace}
	case "ctrl-c":
		return Key{Type: KeyCtrlC}
	case "ctrl-u":
		return Key{Type: KeyCtrlU}
	}
	r := []rune(s)
	if len(r) != 1 {
		panic("bad key " + s)
	}
	return Key{Type: KeyRune, Rune: r[0]}
}

// press feeds each space-separated key name, or each character of a plain word.
func press(m Model, keys ...string) (Model, []Effect) {
	var all []Effect
	for _, s := range keys {
		var e []Effect
		m, e = Update(m, KeyEvent{Key: k(s)})
		all = append(all, e...)
	}
	return m, all
}

func typeText(m Model, s string) Model {
	for _, r := range s {
		m, _ = Update(m, KeyEvent{Key: Key{Type: KeyRune, Rune: r}})
	}
	return m
}

func find[T Effect](effs []Effect) (T, bool) {
	var zero T
	for _, e := range effs {
		if v, ok := e.(T); ok {
			return v, true
		}
	}
	return zero, false
}

func count[T Effect](effs []Effect) int {
	n := 0
	for _, e := range effs {
		if _, ok := e.(T); ok {
			n++
		}
	}
	return n
}

func ids(rs []api.Request) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.ID)
	}
	return strings.Join(s, ",")
}

func TestListSortFollowsDashboard(t *testing.T) {
	m := loaded(newModel(),
		mk("old-done", api.StatusComplete, 1),
		mk("p2", api.StatusPending, 5),
		mk("new-done", api.StatusComplete, 9),
		mk("p1", api.StatusPending, 3),
		mk("run", api.StatusRunning, 7),
	)
	// All: pending first (oldest first), then the rest newest first.
	if got := ids(m.Visible()); got != "p1,p2,new-done,run,old-done" {
		t.Fatalf("all = %s", got)
	}
	m, _ = press(m, "3") // complete: newest first
	if got := ids(m.Visible()); got != "new-done,old-done" {
		t.Fatalf("complete = %s", got)
	}
	m, _ = press(m, "1") // pending: oldest first
	if got := ids(m.Visible()); got != "p1,p2" {
		t.Fatalf("pending = %s", got)
	}
}

func TestFilterKeysCoverEveryStatus(t *testing.T) {
	m := loaded(newModel(),
		mk("a", api.StatusPending, 1), mk("b", api.StatusRunning, 2), mk("c", api.StatusComplete, 3),
		mk("d", api.StatusDenied, 4), mk("e", api.StatusWithdrawn, 5), mk("f", api.StatusError, 6),
		mk("g", api.StatusApproved, 7), mk("h", api.StatusTimeout, 8))
	for key, want := range map[string]string{"1": "a", "2": "b", "3": "c", "4": "d", "5": "e", "6": "f", "7": "g", "8": "h"} {
		m, _ = press(m, key)
		if got := ids(m.Visible()); got != want {
			t.Errorf("filter %s = %s, want %s", key, got, want)
		}
	}
	m, _ = press(m, "0")
	if len(m.Visible()) != 8 {
		t.Fatalf("all = %d", len(m.Visible()))
	}
	m, _ = press(m, "9")
	if m.Filter != FilterWhitelist {
		t.Fatalf("filter = %v", m.Filter)
	}
}

func TestFilterCycleKeys(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "tab")
	if m.Filter != FilterPending {
		t.Fatalf("tab -> %v", m.Filter)
	}
	m, _ = press(m, "backtab", "backtab")
	if m.Filter != FilterWhitelist {
		t.Fatalf("backtab wraps to the whitelist: %v", m.Filter)
	}
	m, _ = press(m, "]")
	if m.Filter != FilterAll {
		t.Fatalf("] from whitelist wraps to all: %v", m.Filter)
	}
}

func TestDisplayLimitAndShowAll(t *testing.T) {
	var rs []api.Request
	for i := 0; i < 25; i++ {
		rs = append(rs, mk(fmt.Sprintf("r%02d", i), api.StatusComplete, i))
	}
	m := loaded(newModel(), rs...)
	if len(m.Shown()) != 20 || len(m.Visible()) != 25 {
		t.Fatalf("shown %d visible %d", len(m.Shown()), len(m.Visible()))
	}
	m, _ = press(m, "s")
	if len(m.Shown()) != 25 {
		t.Fatalf("shown after s = %d", len(m.Shown()))
	}
	m, _ = press(m, "s")
	if len(m.Shown()) != 20 {
		t.Fatal("s again collapses")
	}
	m, _ = press(m, "s", "3") // changing the filter resets show-all
	if m.ShowAll {
		t.Fatal("filter change must reset show-all")
	}
}

func TestSelectionMovesAndClamps(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2), mk("c", api.StatusPending, 3))
	if m.Sel != "a" {
		t.Fatalf("initial selection %q", m.Sel)
	}
	m, _ = press(m, "j", "j", "j", "j")
	if m.Sel != "c" {
		t.Fatalf("clamped at the end: %q", m.Sel)
	}
	m, _ = press(m, "k", "up")
	if m.Sel != "a" {
		t.Fatalf("%q", m.Sel)
	}
	m, _ = press(m, "end")
	if m.Sel != "c" {
		t.Fatalf("end: %q", m.Sel)
	}
	m, _ = press(m, "home")
	if m.Sel != "a" {
		t.Fatalf("home: %q", m.Sel)
	}
}

func TestSelectionStaysOnTheSameRequestAcrossRefetch(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	m, _ = press(m, "j")
	if m.Sel != "b" {
		t.Fatal(m.Sel)
	}
	// An older pending request arrives and sorts above both.
	m = loaded(m, mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2), mk("z", api.StatusPending, 0))
	if m.Sel != "b" {
		t.Fatalf("selection must follow the request, not the row: %q", m.Sel)
	}
}

func TestApproveBindsToTheSelectedIDAndSendsIt(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	m, _ = press(m, "j")
	_, effs := press(m, "enter", "a")
	e, ok := find[DoApprove](effs)
	if !ok || e.ID != "b" || e.Gated || e.ThenWhitelist {
		t.Fatalf("effects = %+v", effs)
	}
}

func TestApproveGatedKey(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	_, effs := press(m, "enter", "g")
	if e, ok := find[DoApprove](effs); !ok || !e.Gated || e.ID != "a" {
		t.Fatalf("effects = %+v", effs)
	}
}

func TestDecisionKeysRefuseNonPending(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusComplete, 1))
	for _, key := range []string{"a", "g", "A", "d"} {
		m2, effs := press(m, key)
		if len(effs) != 0 || m2.Prompt != nil {
			t.Errorf("%s on a complete request produced %+v / prompt %v", key, effs, m2.Prompt)
		}
		if !m2.Status.Err || m2.Status.Text == "" {
			t.Errorf("%s: no explanation shown", key)
		}
	}
}

func TestSelectionJumpGuardRefusesDecisionRightAfterAutoMove(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	// "a" is selected; it gets decided elsewhere and disappears from the list.
	m = loaded(m, mk("b", api.StatusPending, 2))
	if m.Sel != "b" {
		t.Fatalf("selection = %q", m.Sel)
	}
	m2, effs := press(m, "a")
	if len(effs) != 0 {
		t.Fatalf("a decision key right after the selection moved on its own must be refused: %+v", effs)
	}
	if !strings.Contains(m2.Status.Text, "moved") {
		t.Fatalf("status = %q", m2.Status.Text)
	}
	// After the guard window passes it works.
	m.Now = m.Now.Add(2 * time.Second)
	_, effs = press(m, "enter", "a")
	if _, ok := find[DoApprove](effs); !ok {
		t.Fatalf("effects = %+v", effs)
	}
	// Moving the selection by hand never arms the guard.
	m = loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	m, _ = press(m, "j")
	if _, effs := press(m, "enter", "a"); count[DoApprove](effs) != 1 {
		t.Fatalf("manual move must not be guarded: %+v", effs)
	}
}

func TestApproveAndWhitelistNeedsConfirmAndWhitelistsOnlyAfterSuccess(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, effs := press(m, "enter", "A")
	if len(effs) != 0 || m.Prompt == nil || m.Prompt.Kind != PromptApproveWhitelist {
		t.Fatalf("A must open a confirm first: %+v %+v", effs, m.Prompt)
	}
	if !strings.Contains(m.Prompt.Text, "cmd-a") {
		t.Fatalf("confirm should show the command: %q", m.Prompt.Text)
	}
	// n and esc cancel.
	if m2, effs := press(m, "n"); m2.Prompt != nil || len(effs) != 0 {
		t.Fatal("n cancels")
	}
	if m2, effs := press(m, "esc"); m2.Prompt != nil || len(effs) != 0 {
		t.Fatal("esc cancels")
	}
	m, effs = press(m, "y")
	e, ok := find[DoApprove](effs)
	if !ok || !e.ThenWhitelist || e.ID != "a" || m.Prompt != nil {
		t.Fatalf("y approves with the whitelist flag: %+v", effs)
	}
	// The whitelist rule is posted only once the approve returned 2xx.
	_, effs = Update(m, ActionDone{Op: OpApprove, ID: "a", ThenWhitelist: true})
	if w, ok := find[DoAddWhitelist](effs); !ok || w.ID != "a" {
		t.Fatalf("after a successful approve: %+v", effs)
	}
	for _, bad := range []error{
		&api.Error{Status: 429, RemainingMs: 4000},
		&api.Error{Status: 409, Body: "request is already denied"},
		&api.Error{Status: 404, Body: "request not found"},
		&api.Error{Status: 403, Body: "forbidden: this token cannot approve."},
		errors.New("network down"),
	} {
		_, effs = Update(m, ActionDone{Op: OpApprove, ID: "a", ThenWhitelist: true, Err: bad})
		if _, ok := find[DoAddWhitelist](effs); ok {
			t.Fatalf("whitelist must not follow a failed approve (%v)", bad)
		}
	}
}

func TestWhitelistNeverOfferedForRegistryOps(t *testing.T) {
	m := loaded(newModel(), mk("r", api.StatusPending, 1, registry))
	if m2, effs := press(m, "A"); m2.Prompt != nil || len(effs) != 0 || !m2.Status.Err {
		t.Fatalf("A on registry_op: prompt %v effects %+v", m2.Prompt, effs)
	}
	// Plain approve still works for it.
	if _, effs := press(m, "enter", "a"); count[DoApprove](effs) != 1 {
		t.Fatal("approve is fine")
	}
	done := loaded(newModel(), mk("r", api.StatusComplete, 1, registry))
	if m2, _ := press(done, "w"); m2.Prompt != nil || !m2.Status.Err {
		t.Fatal("w on registry_op must be refused")
	}
}

func TestDenyPromptSemantics(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, effs := press(m, "d")
	if m.Prompt == nil || m.Prompt.Kind != PromptDeny || len(effs) != 0 {
		t.Fatalf("d opens the reason prompt: %+v", m.Prompt)
	}
	m = typeText(m, "too riskyX")
	m, _ = press(m, "backspace")
	m, effs = press(m, "enter")
	d, ok := find[DoDeny](effs)
	if !ok || d.ID != "a" || d.Reason != "too risky" || m.Prompt != nil {
		t.Fatalf("effects = %+v", effs)
	}
	// Empty reason is sent as is.
	m = loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "d")
	_, effs = press(m, "enter")
	if d, ok := find[DoDeny](effs); !ok || d.Reason != "" {
		t.Fatalf("empty reason: %+v", effs)
	}
	// Esc cancels and sends nothing.
	m, _ = press(m, "d")
	m = typeText(m, "x")
	m2, effs := press(m, "esc")
	if m2.Prompt != nil || len(effs) != 0 {
		t.Fatalf("esc: %+v %+v", m2.Prompt, effs)
	}
	// Deny keys typed into the prompt are text, not commands.
	m, _ = press(newModel(), "d")
	_ = m
}

func TestPromptTextIsNotInterpretedAsCommands(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "d")
	m = typeText(m, "aqdA9")
	if m.Prompt == nil || m.Prompt.Text != "aqdA9" {
		t.Fatalf("prompt = %+v", m.Prompt)
	}
	if m.Quit {
		t.Fatal("q in a prompt must not quit")
	}
	m, _ = press(m, "ctrl-u")
	if m.Prompt.Text != "" {
		t.Fatal("ctrl-u clears the line")
	}
}

func TestReleaseKey(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusComplete, 1, gated))
	_, effs := press(m, "r")
	if e, ok := find[DoRelease](effs); !ok || e.ID != "a" {
		t.Fatalf("effects = %+v", effs)
	}
	for _, st := range []string{api.StatusRunning, api.StatusError} {
		m := loaded(newModel(), mk("a", st, 1, gated))
		if _, effs := press(m, "r"); count[DoRelease](effs) != 1 {
			t.Errorf("release in status %s", st)
		}
	}
	m = loaded(newModel(), mk("a", api.StatusComplete, 1))
	if m2, effs := press(m, "r"); len(effs) != 0 || !m2.Status.Err {
		t.Fatal("an ungated request cannot be released")
	}
	m = loaded(newModel(), mk("a", api.StatusPending, 1, gated))
	if _, effs := press(m, "r"); len(effs) != 0 {
		t.Fatal("release only for complete, error or running")
	}
}

func TestWhitelistAddConfirmShowsRuleAndRefusesDuplicates(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusComplete, 1))
	m, effs := press(m, "w")
	if len(effs) != 0 || m.Prompt == nil || m.Prompt.Kind != PromptWhitelistAdd {
		t.Fatalf("w opens a confirm: %+v", m.Prompt)
	}
	if !strings.Contains(m.Prompt.Text, "cmd-a a1") {
		t.Fatalf("confirm text = %q", m.Prompt.Text)
	}
	_, effs = press(m, "y")
	if e, ok := find[DoAddWhitelist](effs); !ok || e.ID != "a" {
		t.Fatalf("effects = %+v", effs)
	}
	// Already whitelisted.
	m.Rules = []api.Rule{{Command: "cmd-a", Args: []string{"a1"}}}
	if m2, effs := press(m, "esc", "w"); m2.Prompt != nil || len(effs) != 0 || !strings.Contains(m2.Status.Text, "already") {
		t.Fatalf("duplicate: %+v %q", m2.Prompt, m2.Status.Text)
	}
	// Pending requests get Approve & Whitelist, not w.
	p := loaded(newModel(), mk("p", api.StatusPending, 1))
	if m2, _ := press(p, "w"); m2.Prompt != nil {
		t.Fatal("w is for finished requests")
	}
}

func TestWhitelistConfirmWarnsForHTTP(t *testing.T) {
	m := loaded(newModel(), mk("h", api.StatusComplete, 1, func(r *api.Request) {
		r.Type, r.Command, r.Args = api.TypeHTTP, "", nil
		r.HTTPMethod, r.HTTPURL, r.HTTPBody = "POST", "https://x.example/y", `{"a":1}`
	}))
	m, _ = press(m, "w")
	if m.Prompt == nil || !strings.Contains(m.Prompt.Text, "POST https://x.example/y") || !strings.Contains(m.Prompt.Text, "any body") {
		t.Fatalf("prompt = %+v", m.Prompt)
	}
}

func TestWhitelistViewRemoveFlow(t *testing.T) {
	m := newModel()
	m, _ = Update(m, WhitelistLoaded{Seq: 1, Rules: []api.Rule{{Command: "ls", Args: []string{"-l"}}, {Command: "echo"}}})
	m, _ = press(m, "9", "j")
	m, effs := press(m, "x")
	if len(effs) != 0 || m.Prompt == nil || m.Prompt.Kind != PromptRemoveRule {
		t.Fatalf("x opens a confirm: %+v", m.Prompt)
	}
	if !strings.Contains(m.Prompt.Text, "echo") {
		t.Fatalf("confirm text = %q", m.Prompt.Text)
	}
	_, effs = press(m, "y")
	if e, ok := find[DoRemoveWhitelist](effs); !ok || e.Rule.Command != "echo" {
		t.Fatalf("effects = %+v", effs)
	}
	if m2, effs := press(m, "n"); m2.Prompt != nil || len(effs) != 0 {
		t.Fatal("n cancels")
	}
}

func TestTurboToggle(t *testing.T) {
	m := loaded(newModel())
	_, effs := press(m, "t")
	if e, ok := find[DoTurbo](effs); !ok || !e.On {
		t.Fatalf("off -> on: %+v", effs)
	}
	m.TurboActive, m.TurboUntil = true, m.Now.Add(time.Minute)
	_, effs = press(m, "t")
	if e, ok := find[DoTurbo](effs); !ok || e.On {
		t.Fatalf("on -> off: %+v", effs)
	}
}

func TestClearHistoryAndMarkRead(t *testing.T) {
	m := loaded(newModel(),
		mk("p", api.StatusPending, 1), mk("r", api.StatusRunning, 2), mk("ap", api.StatusApproved, 3),
		mk("c", api.StatusComplete, 4), mk("d", api.StatusDenied, 5), mk("e", api.StatusError, 6),
		mk("t", api.StatusTimeout, 7), mk("w", api.StatusWithdrawn, 8))
	m, _ = press(m, "C")
	if got := ids(m.Visible()); got != "p,ap,r" {
		t.Fatalf("after clear: %s", got)
	}
	// The hidden set survives a refetch.
	m = loaded(m, mk("p", api.StatusPending, 1), mk("c", api.StatusComplete, 4))
	if got := ids(m.Visible()); got != "p" {
		t.Fatalf("hidden ids must stay hidden: %s", got)
	}
	// The pending count still counts everything fetched, hidden or not.
	if m.PendingCount() != 1 || m.Total() != 2 {
		t.Fatalf("pending %d total %d", m.PendingCount(), m.Total())
	}
	// Mark read hides a withdrawn request only.
	m = loaded(newModel(), mk("w", api.StatusWithdrawn, 1), mk("c", api.StatusComplete, 2))
	m, _ = press(m, "j", "m")
	if ids(m.Visible()) != "c" {
		t.Fatalf("mark read: %s", ids(m.Visible()))
	}
	if m2, _ := press(m, "m"); !m2.Status.Err {
		t.Fatal("mark read on a non-withdrawn request is refused")
	}
}

func TestClearHistoryAppliesToEveryFilterButWhitelist(t *testing.T) {
	m := loaded(newModel(), mk("c", api.StatusComplete, 1))
	m, _ = press(m, "C", "3")
	if len(m.Visible()) != 0 {
		t.Fatal("hidden applies to the complete filter")
	}
	m, _ = Update(m, WhitelistLoaded{Seq: 9, Rules: []api.Rule{{Command: "ls"}}})
	m, _ = press(m, "9")
	if len(m.Rules) != 1 {
		t.Fatal("whitelist is unaffected")
	}
}

func TestClearHistoryWithNothingToClear(t *testing.T) {
	m := loaded(newModel(), mk("p", api.StatusPending, 1))
	m2, _ := press(m, "C")
	if len(m2.Hidden) != 0 {
		t.Fatal("nothing to hide")
	}
}

func TestCooldownFromListHeadersAndExpiry(t *testing.T) {
	m := newModel()
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: &api.Listing{
		CooldownRemaining: 12 * time.Second, CooldownDuration: 30 * time.Second}})
	if m.CooldownLeft() != 12*time.Second || m.CooldownDuration != 30*time.Second {
		t.Fatalf("cooldown = %v / %v", m.CooldownLeft(), m.CooldownDuration)
	}
	m, _ = Update(m, TickEvent{Now: t0.Add(5 * time.Second)})
	if m.CooldownLeft() != 7*time.Second {
		t.Fatalf("after 5s: %v", m.CooldownLeft())
	}
	m, _ = Update(m, TickEvent{Now: t0.Add(20 * time.Second)})
	if m.CooldownLeft() != 0 {
		t.Fatalf("expired: %v", m.CooldownLeft())
	}
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: &api.Listing{}})
	if m.CooldownLeft() != 0 {
		t.Fatal("zero remaining clears the cooldown")
	}
}

func TestCooldown429SetsCountdownAndExplains(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, effs := Update(m, ActionDone{Op: OpApprove, ID: "a", Err: &api.Error{Status: 429, RemainingMs: 6400, Body: `{"error":"cooldown active","remaining_ms":6400}`}})
	if m.CooldownLeft() != 6400*time.Millisecond {
		t.Fatalf("cooldown = %v", m.CooldownLeft())
	}
	if !m.Status.Err || !strings.Contains(m.Status.Text, "cooldown") || !strings.Contains(m.Status.Text, "7s") {
		t.Fatalf("status = %q", m.Status.Text)
	}
	if _, ok := find[FetchList](effs); !ok {
		t.Fatalf("a 429 refetches to learn the real state: %+v", effs)
	}
	// Falls back to Retry-After when the body has no figure.
	m, _ = Update(loaded(newModel(), mk("a", api.StatusPending, 1)), ActionDone{Op: OpApprove, ID: "a", Err: &api.Error{Status: 429, RetryAfter: 5 * time.Second}})
	if m.CooldownLeft() != 5*time.Second {
		t.Fatalf("retry-after: %v", m.CooldownLeft())
	}
}

func TestTurboFromListHeaders(t *testing.T) {
	m := newModel()
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: &api.Listing{TurboActive: true, TurboRemaining: 90 * time.Second}})
	if !m.TurboActive || m.TurboLeft() != 90*time.Second {
		t.Fatalf("turbo %v %v", m.TurboActive, m.TurboLeft())
	}
	m, effs := Update(m, TickEvent{Now: t0.Add(2 * time.Minute)})
	if m.TurboActive {
		t.Fatal("turbo expired")
	}
	if _, ok := find[FetchList](effs); !ok {
		t.Fatalf("expiry refetches: %+v", effs)
	}
}

func TestConnectionEventsRefetch(t *testing.T) {
	m := newModel()
	m, effs := Update(m, ConnEvent{Up: true})
	if m.Conn != ConnUp || count[FetchList](effs) != 1 || count[FetchWhitelist](effs) != 1 {
		t.Fatalf("up: conn %v effects %+v", m.Conn, effs)
	}
	m, effs = Update(m, ConnEvent{Up: false})
	if m.Conn != ConnDown || len(effs) != 0 {
		t.Fatalf("down: conn %v effects %+v", m.Conn, effs)
	}
	// The answers to the fetches the reconnect asked for arrive first.
	m, _ = Update(m, ListLoaded{Seq: m.ListSeq, Listing: listing()})
	m, _ = Update(m, WhitelistLoaded{Seq: m.WLSeq})
	_, effs = Update(m, FrameEvent{Frame: api.Frame{Type: "update", RequestID: "x"}})
	if count[FetchList](effs) != 1 || count[FetchWhitelist](effs) != 1 {
		t.Fatalf("a frame refetches both lists: %+v", effs)
	}
}

func TestPeriodicPollRefetches(t *testing.T) {
	m := newModel()
	m, effs := Update(m, TickEvent{Now: t0})
	if len(effs) != 0 {
		t.Fatalf("first tick only arms the poll: %+v", effs)
	}
	_, effs = Update(m, TickEvent{Now: t0.Add(PollEvery - time.Second)})
	if len(effs) != 0 {
		t.Fatalf("too early: %+v", effs)
	}
	m, effs = Update(m, TickEvent{Now: t0.Add(PollEvery + time.Second)})
	if count[FetchList](effs) != 1 || count[FetchWhitelist](effs) != 1 {
		t.Fatalf("poll: %+v", effs)
	}
	_, effs = Update(m, TickEvent{Now: t0.Add(PollEvery + 2*time.Second)})
	if len(effs) != 0 {
		t.Fatalf("poll must re-arm: %+v", effs)
	}
}

func TestStaleListResponsesAreIgnored(t *testing.T) {
	m := newModel()
	m, _ = Update(m, ListLoaded{Seq: 10, Listing: listing(mk("new", api.StatusPending, 1))})
	m, _ = Update(m, ListLoaded{Seq: 7, Listing: listing(mk("old", api.StatusPending, 1))})
	if ids(m.Requests) != "new" {
		t.Fatalf("requests = %s", ids(m.Requests))
	}
}

func TestBellOnlyForNewPendingAfterFirstLoad(t *testing.T) {
	m := newModel()
	seq++
	m, effs := Update(m, ListLoaded{Seq: seq, Listing: listing(mk("a", api.StatusPending, 1))})
	if count[RingBell](effs) != 0 {
		t.Fatal("first load must not ring")
	}
	seq++
	m, effs = Update(m, ListLoaded{Seq: seq, Listing: listing(mk("a", api.StatusPending, 1))})
	if count[RingBell](effs) != 0 {
		t.Fatal("no change, no bell")
	}
	seq++
	m, effs = Update(m, ListLoaded{Seq: seq, Listing: listing(mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))})
	if count[RingBell](effs) != 1 {
		t.Fatalf("a new pending request rings once: %+v", effs)
	}
	seq++
	m, effs = Update(m, ListLoaded{Seq: seq, Listing: listing(mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2), mk("c", api.StatusPending, 3), mk("d", api.StatusPending, 4))})
	if count[RingBell](effs) != 1 {
		t.Fatalf("several new ones still ring once: %+v", effs)
	}
	m, _ = press(m, "B")
	if m.Bell {
		t.Fatal("B toggles the bell off")
	}
	seq++
	_, effs = Update(m, ListLoaded{Seq: seq, Listing: listing(mk("e", api.StatusPending, 5))})
	if count[RingBell](effs) != 0 {
		t.Fatal("bell is off")
	}
}

func TestNotApproverOffersTokenReentry(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = Update(m, ActionDone{Op: OpApprove, ID: "a", Err: &api.Error{Status: 403, Body: "forbidden: this token cannot approve. Approving"}})
	if m.Prompt == nil || m.Prompt.Kind != PromptReauth {
		t.Fatalf("prompt = %+v", m.Prompt)
	}
	// n leaves things as they are.
	if m2, effs := press(m, "n"); m2.Prompt != nil || len(effs) != 0 || m2.Status.Text == "" {
		t.Fatalf("n: %+v %+v", m2.Prompt, effs)
	}
	m, effs := press(m, "y")
	if _, ok := find[PurgeToken](effs); !ok {
		t.Fatalf("y purges the held token: %+v", effs)
	}
	if m.Prompt == nil || m.Prompt.Kind != PromptToken {
		t.Fatalf("then asks for the new one: %+v", m.Prompt)
	}
}

func TestTokenPromptIsMaskedAndSubmits(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, effs := press(m, "P")
	if _, ok := find[PurgeToken](effs); !ok || m.Prompt == nil || m.Prompt.Kind != PromptToken {
		t.Fatalf("P: %+v %+v", effs, m.Prompt)
	}
	if len(m.Requests) != 0 {
		t.Fatal("purging the token clears the displayed requests")
	}
	m = typeText(m, "mhra_secret")
	if m.Prompt.Text != "" {
		t.Fatal("the token must never be stored as display text")
	}
	if string(m.Prompt.Secret) != "mhra_secret" {
		t.Fatalf("secret = %q", m.Prompt.Secret)
	}
	m, _ = press(m, "backspace")
	if string(m.Prompt.Secret) != "mhra_secre" {
		t.Fatalf("secret = %q", m.Prompt.Secret)
	}
	m, effs = press(m, "enter")
	c, ok := find[CheckToken](effs)
	if !ok || string(c.Token) != "mhra_secre" {
		t.Fatalf("effects = %+v", effs)
	}
	if m.Prompt != nil && len(m.Prompt.Secret) != 0 {
		t.Fatal("the model must not keep the secret after submitting")
	}
	// Empty token does nothing.
	m, _ = press(newModel(), "P")
	if _, effs := press(m, "enter"); count[CheckToken](effs) != 0 {
		t.Fatal("empty token submitted")
	}
}

func TestTokenCheckedResults(t *testing.T) {
	m, _ := press(newModel(), "P")
	m, effs := Update(m, TokenChecked{})
	if !m.HaveToken || m.Prompt != nil || count[FetchList](effs) != 1 {
		t.Fatalf("accepted: have %v prompt %+v effects %+v", m.HaveToken, m.Prompt, effs)
	}
	for _, bad := range []error{
		&api.Error{Status: 403, Body: "forbidden: this token cannot approve."},
		&api.Error{Status: 401, Body: "unauthorized"},
	} {
		m, _ := press(newModel(), "P")
		m, _ = Update(m, TokenChecked{Err: bad})
		if m.HaveToken || m.Prompt == nil || m.Prompt.Kind != PromptToken || !m.Status.Err {
			t.Fatalf("rejected %v: have %v prompt %+v status %+v", bad, m.HaveToken, m.Prompt, m.Status)
		}
	}
}

func TestUnauthorizedListSuggestsNewToken(t *testing.T) {
	m := newModel()
	m, _ = Update(m, ListLoaded{Seq: 50, Err: &api.Error{Status: 401, Body: "unauthorized"}})
	if !m.Status.Err || !strings.Contains(m.Status.Text, "401") || !strings.Contains(m.Status.Text, "P") {
		t.Fatalf("status = %q", m.Status.Text)
	}
	if m.FetchErr == nil {
		t.Fatal("a failed fetch is remembered for the header")
	}
	m, _ = Update(m, ListLoaded{Seq: 51, Listing: listing()})
	if m.FetchErr != nil {
		t.Fatal("a good fetch clears it")
	}
}

func TestActionErrorsShowInStatusLine(t *testing.T) {
	cases := map[int]string{404: "404", 409: "409", 401: "401", 500: "500"}
	for status, want := range cases {
		m := loaded(newModel(), mk("a", api.StatusPending, 1))
		m, effs := Update(m, ActionDone{Op: OpApprove, ID: "a", Err: &api.Error{Status: status, Body: "boom"}})
		if !m.Status.Err || !strings.Contains(m.Status.Text, want) || !strings.Contains(m.Status.Text, "boom") {
			t.Errorf("%d: status %q", status, m.Status.Text)
		}
		if count[FetchList](effs) != 1 {
			t.Errorf("%d: no refetch", status)
		}
	}
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = Update(m, ActionDone{Op: OpApprove, ID: "a", Err: errors.New("dial tcp: refused")})
	if !strings.Contains(m.Status.Text, "refused") {
		t.Fatalf("status = %q", m.Status.Text)
	}
}

func TestSuccessfulActionsRefetchAndConfirmInStatus(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	for op, word := range map[ActionOp]string{OpApprove: "approved", OpApproveGated: "gated", OpDeny: "denied", OpRelease: "released"} {
		m2, effs := Update(m, ActionDone{Op: op, ID: "a"})
		if m2.Status.Err || !strings.Contains(strings.ToLower(m2.Status.Text), word) || !strings.Contains(m2.Status.Text, "cmd-a") {
			t.Errorf("op %v: status %q", op, m2.Status.Text)
		}
		if count[FetchList](effs) != 1 {
			t.Errorf("op %v: no refetch", op)
		}
	}
	_, effs := Update(m, ActionDone{Op: OpWhitelistAdd, ID: "a"})
	if count[FetchWhitelist](effs) != 1 {
		t.Fatalf("whitelist add refetches the whitelist: %+v", effs)
	}
	_, effs = Update(m, ActionDone{Op: OpWhitelistRemove})
	if count[FetchWhitelist](effs) != 1 {
		t.Fatalf("whitelist remove refetches the whitelist: %+v", effs)
	}
}

func TestFullContentFetchStatesAndRetry(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, stdin(2048)))
	m, _ = press(m, "enter")
	m, effs := press(m, "f")
	if e, ok := find[FetchContent](effs); !ok || e.ID != "a" {
		t.Fatalf("effects = %+v", effs)
	}
	if m.Content["a"] == nil || m.Content["a"].State != ContentLoading {
		t.Fatalf("content = %+v", m.Content["a"])
	}
	// A failure is shown and does not stick: f tries again.
	m, _ = Update(m, ContentLoaded{ID: "a", Err: &api.Error{Status: 500, Body: "x"}})
	if m.Content["a"].State != ContentFailed || !strings.Contains(m.Content["a"].Err, "500") {
		t.Fatalf("content = %+v", m.Content["a"])
	}
	m, effs = press(m, "f")
	if count[FetchContent](effs) != 1 {
		t.Fatal("a failed fetch can be retried")
	}
	m, _ = Update(m, ContentLoaded{ID: "a", Content: &api.Content{Body: []byte("l1\nl2\n"), SHA256: "abc"}})
	if m.Content["a"].State != ContentReady || !m.Content["a"].Expanded {
		t.Fatalf("content = %+v", m.Content["a"])
	}
	// f toggles collapse without refetching.
	m, effs = press(m, "f")
	if m.Content["a"].Expanded || len(effs) != 0 {
		t.Fatalf("f collapses: %+v %+v", m.Content["a"], effs)
	}
	m, effs = press(m, "f")
	if !m.Content["a"].Expanded || len(effs) != 0 {
		t.Fatal("f expands again from cache")
	}
}

func TestContentCachePrunedToListedIDs(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, stdin(10)), mk("b", api.StatusPending, 2, stdin(10)))
	m.Content = map[string]*ContentState{"a": {State: ContentReady}, "gone": {State: ContentReady}}
	m = loaded(m, mk("a", api.StatusPending, 1, stdin(10)))
	if _, ok := m.Content["gone"]; ok {
		t.Fatal("content for an id that left the list must be dropped")
	}
	if _, ok := m.Content["a"]; !ok {
		t.Fatal("content for a listed id stays")
	}
}

func TestFullKeyWithoutStdinSaysSo(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "enter")
	m2, effs := press(m, "f")
	if len(effs) != 0 || m2.Status.Text == "" {
		t.Fatalf("%+v %q", effs, m2.Status.Text)
	}
}

func TestDetailOpenCloseAndScroll(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusComplete, 1, func(r *api.Request) {
		r.Result = &api.Result{Stdout: strings.Repeat("line\n", 80)}
	}))
	m, _ = press(m, "enter")
	if m.Mode != ModeDetail {
		t.Fatal("enter opens the detail view")
	}
	m, _ = press(m, "j", "j", "j")
	if m.DetailTop != 3 {
		t.Fatalf("scroll = %d", m.DetailTop)
	}
	m, _ = press(m, "k")
	if m.DetailTop != 2 {
		t.Fatalf("scroll = %d", m.DetailTop)
	}
	m, _ = press(m, "esc")
	if m.Mode != ModeList || m.DetailTop != 0 {
		t.Fatalf("esc returns to the list: mode %v top %d", m.Mode, m.DetailTop)
	}
	m, _ = press(m, "l")
	if m.Mode != ModeDetail {
		t.Fatal("l opens too")
	}
	m, _ = press(m, "left")
	if m.Mode != ModeList {
		t.Fatal("left goes back")
	}
}

func TestDetailVanishedRequestReturnsToList(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	m, _ = press(m, "enter")
	m = loaded(m, mk("b", api.StatusPending, 2))
	if m.Mode != ModeList {
		t.Fatalf("mode = %v", m.Mode)
	}
}

func TestDetailTogglesPersistAcrossRefetch(t *testing.T) {
	long := strings.Repeat("r", 300)
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Reason = long }))
	m, _ = press(m, "enter", "e", "b", "p", "H")
	m = loaded(m, mk("a", api.StatusPending, 1, func(r *api.Request) { r.Reason = long }))
	if !m.ReasonOpen["a"] || !m.B64Collapsed["a"] || !m.PSRaw["a"] || !m.RespHeaders["a"] {
		t.Fatalf("toggles lost: %v %v %v %v", m.ReasonOpen, m.B64Collapsed, m.PSRaw, m.RespHeaders)
	}
}

func TestHelpAndQuit(t *testing.T) {
	m := newModel()
	m, _ = press(m, "?")
	if m.Mode != ModeHelp {
		t.Fatal("? opens help")
	}
	m, _ = press(m, "esc")
	if m.Mode != ModeList {
		t.Fatal("esc closes help")
	}
	m, effs := press(m, "q")
	if _, ok := find[Quit](effs); !ok || !m.Quit {
		t.Fatalf("q quits: %+v", effs)
	}
	m, effs = press(newModel(), "ctrl-c")
	if _, ok := find[Quit](effs); !ok {
		t.Fatal("ctrl-c quits")
	}
	m, _ = press(newModel(), "d")
	_ = m
}

func TestCtrlCQuitsEvenInAPrompt(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "d")
	_, effs := press(m, "ctrl-c")
	if _, ok := find[Quit](effs); !ok {
		t.Fatal("ctrl-c must always quit")
	}
}

func TestRefreshKey(t *testing.T) {
	_, effs := press(newModel(), "R")
	if count[FetchList](effs) != 1 || count[FetchWhitelist](effs) != 1 {
		t.Fatalf("%+v", effs)
	}
}

func TestResizeEvent(t *testing.T) {
	m, _ := Update(newModel(), ResizeEvent{W: 50, H: 12})
	if m.Width != 50 || m.Height != 12 {
		t.Fatalf("%d x %d", m.Width, m.Height)
	}
}

func TestStatusExpires(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusComplete, 1))
	m, _ = press(m, "a") // refused: sets an error status
	if m.Status.Text == "" {
		t.Fatal("no status")
	}
	m, _ = Update(m, TickEvent{Now: t0.Add(30 * time.Second)})
	if m.Status.Text != "" {
		t.Fatalf("status should expire, still %q", m.Status.Text)
	}
}

func TestPendingCountAndTotals(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusComplete, 2))
	if m.PendingCount() != 1 || m.Total() != 2 {
		t.Fatalf("%d %d", m.PendingCount(), m.Total())
	}
}

func TestKeysIgnoredWhileNoToken(t *testing.T) {
	m := newModel()
	m.HaveToken = false
	m, effs := press(m, "a", "t", "R")
	if len(effs) != 0 {
		t.Fatalf("no token, no network calls: %+v", effs)
	}
}

func TestInitEffects(t *testing.T) {
	m := newModel()
	m, effs := Init(m)
	if count[FetchList](effs) != 1 || count[FetchWhitelist](effs) != 1 {
		t.Fatalf("%+v", effs)
	}
	_ = m
}

func TestSelectionAdvancesToTheNextPendingAfterMyDecision(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2), mk("c", api.StatusPending, 3))
	m, _ = Update(m, ActionDone{Op: OpApprove, ID: "a"})
	// The refetch that shows the decision.
	m = loaded(m, mk("a", api.StatusApproved, 1), mk("b", api.StatusPending, 2), mk("c", api.StatusPending, 3))
	if m.Sel != "b" {
		t.Fatalf("selection should move to the next pending request, got %q", m.Sel)
	}
	// The move arms the guard: a second key press at once does not decide b.
	if _, effs := press(m, "a"); len(effs) != 0 {
		t.Fatalf("decision right after the selection moved on must wait: %+v", effs)
	}
}

func TestSelectionStaysWhenNothingElseIsPending(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = Update(m, ActionDone{Op: OpDeny, ID: "a"})
	m = loaded(m, mk("a", api.StatusDenied, 1))
	if m.Sel != "a" {
		t.Fatalf("selection = %q", m.Sel)
	}
}

func TestAdvanceWaitsUntilTheDecisionIsListed(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	m, _ = Update(m, ActionDone{Op: OpApprove, ID: "a"})
	// A refetch that raced the approve still shows a pending.
	m = loaded(m, mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	if m.Sel != "a" {
		t.Fatalf("must not move while a is still pending: %q", m.Sel)
	}
	m = loaded(m, mk("a", api.StatusComplete, 1), mk("b", api.StatusPending, 2))
	if m.Sel != "b" {
		t.Fatalf("selection = %q", m.Sel)
	}
}

func TestAdvanceOnlyWhenTheDecidedRequestWasSelected(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	m, _ = press(m, "j") // b selected
	m, _ = Update(m, ActionDone{Op: OpApprove, ID: "a"})
	m = loaded(m, mk("a", api.StatusComplete, 1), mk("b", api.StatusPending, 2))
	if m.Sel != "b" {
		t.Fatalf("selection = %q", m.Sel)
	}
}

func TestDetailLastLineIsReachable(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusComplete, 1, func(r *api.Request) {
		r.Result = &api.Result{Stdout: strings.Repeat("filler\n", 60) + "THE-LAST-LINE"}
	}))
	m.Height = 20
	m, _ = press(m, "enter", "end")
	out := Render(m, Theme{})
	if !strings.Contains(out, "THE-LAST-LINE") {
		t.Fatalf("last content line hidden behind the position marker:\n%s", out)
	}
}

func TestAdvancePicksTheNextPendingBelowNotTheTopOne(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2), mk("c", api.StatusPending, 3), mk("d", api.StatusPending, 4))
	m, _ = press(m, "j") // b
	m, _ = Update(m, ActionDone{Op: OpApprove, ID: "b"})
	m = loaded(m, mk("a", api.StatusPending, 1), mk("b", api.StatusApproved, 2), mk("c", api.StatusPending, 3), mk("d", api.StatusPending, 4))
	if m.Sel != "c" {
		t.Fatalf("selection = %q, want c", m.Sel)
	}
	// Last one: falls back to the nearest above.
	m = loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	m, _ = press(m, "j")
	m, _ = Update(m, ActionDone{Op: OpDeny, ID: "b"})
	m = loaded(m, mk("a", api.StatusPending, 1), mk("b", api.StatusDenied, 2))
	if m.Sel != "a" {
		t.Fatalf("selection = %q, want a", m.Sel)
	}
}

func TestNewRequestIsAnnouncedInTheStatusLine(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	if strings.Contains(m.Status.Text, "new request") {
		t.Fatal("the first load announces nothing")
	}
	long := strings.Repeat("why ", 60)
	m = loaded(m, mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2, func(r *api.Request) { r.Reason = long }))
	if !strings.HasPrefix(m.Status.Text, "new request: cmd-b - why") {
		t.Fatalf("status = %q", m.Status.Text)
	}
	if n := len([]rune(strings.TrimPrefix(m.Status.Text, "new request: "))); n > 103 {
		t.Fatalf("announcement is %d characters, want at most 100 plus ...", n)
	}
	m = loaded(m, mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2, func(r *api.Request) { r.Reason = long }), mk("h", api.StatusPending, 3, func(r *api.Request) {
		r.Type, r.Command, r.HTTPMethod, r.HTTPURL = api.TypeHTTP, "", "POST", "https://x.test/y"
	}))
	if !strings.Contains(m.Status.Text, "POST https://x.test/y") {
		t.Fatalf("status = %q", m.Status.Text)
	}
}

func TestPastedTextNeverActsAsCommands(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = Update(m, KeyEvent{Key: Key{Type: KeyPasteStart}})
	var effs []Effect
	for _, r := range "aAgdqw\n" {
		kk := Key{Type: KeyRune, Rune: r}
		if r == '\n' {
			kk = Key{Type: KeyEnter}
		}
		var e []Effect
		m, e = Update(m, KeyEvent{Key: kk})
		effs = append(effs, e...)
	}
	m, _ = Update(m, KeyEvent{Key: Key{Type: KeyPasteEnd}})
	if len(effs) != 0 || m.Prompt != nil || m.Quit || m.Mode != ModeList {
		t.Fatalf("pasted text acted: effects %+v prompt %+v quit %v mode %v", effs, m.Prompt, m.Quit, m.Mode)
	}
	// After the paste ends, keys work again.
	if _, effs := press(m, "enter", "a"); count[DoApprove](effs) != 1 {
		t.Fatal("keys must work after the paste")
	}
}

func TestPastedTextGoesIntoTextPromptsButNeverSubmitsThem(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "d")
	m, _ = Update(m, KeyEvent{Key: Key{Type: KeyPasteStart}})
	m = typeText(m, "line one")
	var effs []Effect
	m, effs = Update(m, KeyEvent{Key: Key{Type: KeyEnter}})
	if len(effs) != 0 || m.Prompt == nil || m.Prompt.Text != "line one" {
		t.Fatalf("a newline inside a paste must not submit: %+v %+v", effs, m.Prompt)
	}
	m, _ = Update(m, KeyEvent{Key: Key{Type: KeyPasteEnd}})
	// A confirmation is never answered by pasted text.
	m = loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "enter", "A")
	m, _ = Update(m, KeyEvent{Key: Key{Type: KeyPasteStart}})
	m, effs = Update(m, KeyEvent{Key: Key{Type: KeyRune, Rune: 'y'}})
	if len(effs) != 0 || m.Prompt == nil {
		t.Fatalf("pasted y answered a confirmation: %+v", effs)
	}
	// The token prompt takes pasted text.
	m, _ = press(newModel(), "P")
	m, _ = Update(m, KeyEvent{Key: Key{Type: KeyPasteStart}})
	m = typeText(m, "mhra_pasted")
	if string(m.Prompt.Secret) != "mhra_pasted" {
		t.Fatalf("secret = %q", m.Prompt.Secret)
	}
}

func TestNothingBeingApprovedIsCutOff(t *testing.T) {
	longLine := strings.Repeat("0123456789", 3000) + "END-OF-LONG"
	var many strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&many, "row %d\n", i)
	}
	many.WriteString("END-OF-MANY")
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) {
		r.Command, r.Args = "sh", []string{longLine, many.String()}
		r.DisplayCommand = many.String()
		r.Reason = many.String()
	}))
	var sb strings.Builder
	for _, l := range DetailLines(m, m.Requests[0], 80) {
		sb.WriteString(l.Plain())
		sb.WriteString("\n")
	}
	flat := strings.ReplaceAll(sb.String(), "\n", "")
	flat = strings.ReplaceAll(flat, " ", "")
	if strings.Count(flat, "END-OF-LONG") != 1 || strings.Count(flat, "END-OF-MANY") < 2 {
		t.Fatalf("the end of a long argument was cut: long=%d many=%d", strings.Count(flat, "END-OF-LONG"), strings.Count(flat, "END-OF-MANY"))
	}
	if !strings.Contains(sb.String(), "row 2999") || strings.Contains(sb.String(), "more lines not shown") || strings.Contains(sb.String(), "line cut") {
		t.Fatal("a cut marker appeared")
	}
}

func TestLargeContentScrollsWithoutRelayout(t *testing.T) {
	big := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 120000) // about 5 MB
	m := loaded(newModel(), mk("a", api.StatusPending, 1, stdin(len(big))))
	m, _ = press(m, "enter", "f")
	m, _ = Update(m, ContentLoaded{ID: "a", Content: &api.Content{Body: []byte(big)}})
	start := time.Now()
	DetailLines(m, m.Requests[0], m.Width)
	first := time.Since(start)
	start = time.Now()
	for i := 0; i < 50; i++ {
		m, _ = press(m, "j", "pgdn")
		_ = Render(m, Theme{})
	}
	scrolling := time.Since(start)
	if scrolling > 2*time.Second {
		t.Fatalf("100 scroll keys with a redraw each took %v (first layout %v)", scrolling, first)
	}
	// A clock tick does not lay the request out again either.
	start = time.Now()
	for i := 0; i < 20; i++ {
		m, _ = Update(m, TickEvent{Now: m.Now.Add(250 * time.Millisecond)})
		_ = Render(m, Theme{})
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("20 ticks took %v", d)
	}
}

func TestLayoutCacheFollowsToggles(t *testing.T) {
	long := strings.Repeat("word ", 80)
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Reason = long }))
	before := len(DetailLines(m, m.Requests[0], 100))
	m, _ = press(m, "enter", "e")
	after := len(DetailLines(m, m.Requests[0], 100))
	if after <= before {
		t.Fatalf("expanding the reason must change the layout (%d -> %d lines)", before, after)
	}
	// Different width, same request: laid out again.
	if len(DetailLines(m, m.Requests[0], 30)) <= after {
		t.Fatal("a narrower width needs more lines")
	}
}
