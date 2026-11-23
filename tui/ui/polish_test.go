package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/tui/api"
)

// --- hints ---

func TestWithdrawnRequestsHintWhitelisting(t *testing.T) {
	m := loaded(newModel(), mk("w", api.StatusWithdrawn, 1))
	hints := strings.Join(m.actionHints(), "|")
	if !strings.Contains(hints, "m mark read") || !strings.Contains(hints, "w whitelist") {
		t.Fatalf("hints = %q", hints)
	}
	m2, _ := press(m, "w")
	if m2.Prompt == nil || m2.Prompt.Kind != PromptWhitelistAdd {
		t.Fatal("w on a withdrawn request asks to whitelist it")
	}
	reg := loaded(newModel(), mk("r", api.StatusWithdrawn, 1, registry))
	if strings.Contains(strings.Join(reg.actionHints(), "|"), "whitelist") {
		t.Fatal("registry operations are never offered for whitelisting")
	}
}

func httpReq(body string) api.Request {
	return mk("h", api.StatusPending, 1, func(r *api.Request) {
		r.Type, r.Command, r.Args = api.TypeHTTP, "", nil
		r.HTTPMethod, r.HTTPURL, r.HTTPBody = "POST", "https://x.test/y", body
	})
}

func TestFullHintOnlyWhenThereIsMoreToShow(t *testing.T) {
	short := loaded(newModel(), httpReq(`{"a":1}`))
	short, _ = press(short, "enter")
	if h := strings.Join(short.actionHints(), "|"); strings.Contains(h, "f full") {
		t.Fatalf("a short body has nothing more to show: %q", h)
	}
	short, _ = press(short, "f")
	if !strings.Contains(short.Status.Text, "nothing more") {
		t.Fatalf("status = %q", short.Status.Text)
	}
	long := loaded(newModel(), httpReq(strings.Repeat("x", 3000)))
	long, _ = press(long, "enter")
	if h := strings.Join(long.actionHints(), "|"); !strings.Contains(h, "f full") {
		t.Fatalf("a cut body offers the whole: %q", h)
	}
	long, _ = press(long, "f")
	if h := strings.Join(long.actionHints(), "|"); !strings.Contains(h, "f full") {
		t.Fatalf("with the whole body shown, f still folds it back: %q", h)
	}
}

// --- the status line ---

func TestNewRequestDoesNotReplaceTheResultOfMyOwnAction(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = Update(m, ActionDone{Op: OpApprove, ID: "a"})
	m.Now = m.Now.Add(time.Second)
	var effs []Effect
	seq++
	m, effs = Update(m, ListLoaded{Seq: seq, Listing: listing(mk("a", api.StatusComplete, 1), mk("b", api.StatusPending, 2))})
	if !strings.HasPrefix(m.Status.Text, "approved") {
		t.Fatalf("status = %q", m.Status.Text)
	}
	if count[RingBell](effs) != 1 {
		t.Fatalf("the bell still rings for the new request: %+v", effs)
	}
	// Once the result has been on screen a while, a new request shows.
	m.Now = m.Now.Add(10 * time.Second)
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: listing(mk("a", api.StatusComplete, 1), mk("b", api.StatusPending, 2), mk("c", api.StatusPending, 3))})
	if !strings.HasPrefix(m.Status.Text, "new request") {
		t.Fatalf("status = %q", m.Status.Text)
	}
	// Errors from actions are kept the same way.
	m, _ = Update(m, ActionDone{Op: OpDeny, ID: "b", Err: &api.Error{Status: 409, Body: "already"}})
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: listing(mk("a", api.StatusComplete, 1), mk("b", api.StatusPending, 2), mk("c", api.StatusPending, 3), mk("d", api.StatusPending, 4))})
	if !m.Status.Err || !strings.Contains(m.Status.Text, "deny failed") {
		t.Fatalf("status = %q", m.Status.Text)
	}
}

func TestSeveralNewRequestsAreCountedInOneAnnouncement(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m = loaded(m, mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2), mk("c", api.StatusPending, 3), mk("d", api.StatusPending, 4))
	if !strings.HasPrefix(m.Status.Text, "new request: cmd-b") || !strings.Contains(m.Status.Text, "+2 more") {
		t.Fatalf("status = %q", m.Status.Text)
	}
}

func TestOpeningADialogClearsAStaleMessage(t *testing.T) {
	for _, key := range []string{"d", "w", "A"} {
		status := api.StatusPending
		if key == "w" {
			status = api.StatusComplete
		}
		m := loaded(newModel(), mk("a", status, 1))
		m, _ = press(m, "enter")
		m.fail("cannot decide: request is not pending (error)")
		m, _ = press(m, key)
		if m.Prompt == nil {
			t.Fatalf("%s opened no prompt", key)
		}
		if m.Status.Text != "" {
			t.Fatalf("%s: a stale message stays under the dialog: %q", key, m.Status.Text)
		}
	}
	// The re-enter dialog keeps the message that explains it.
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = Update(m, ActionDone{Op: OpApprove, ID: "a", Err: &api.Error{Status: 403, Body: "forbidden: this token cannot approve."}})
	if m.Prompt == nil || m.Prompt.Kind != PromptReauth || !strings.Contains(m.Status.Text, "refused") {
		t.Fatalf("prompt %+v status %q", m.Prompt, m.Status.Text)
	}
}

// --- a rejected token ---

func TestHeaderAndBodySayWhenTheTokenIsRejected(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	if !strings.Contains(Render(m, Theme{}), "token ok") && m.TokenNote == "" {
		m.TokenNote = "token ok: approver"
	}
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Err: &api.Error{Status: 401, Body: "unauthorized"}})
	scr := Render(m, Theme{})
	first := strings.SplitN(scr, "\n", 2)[0]
	if strings.Contains(first, "token ok") || !strings.Contains(first, "token rejected") {
		t.Fatalf("header = %q", first)
	}
	if !strings.Contains(scr, "press P") {
		t.Fatalf("the screen says what to do:\n%s", scr)
	}
	m = loaded(newModel())
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Err: &api.Error{Status: 401}})
	if scr := Render(m, Theme{}); strings.Contains(scr, "No requests") {
		t.Fatalf("an empty list that could not be fetched is not 'No requests':\n%s", scr)
	}
	// A good fetch brings the note back.
	m.TokenNote = "token ok: approver"
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: listing(mk("a", api.StatusPending, 1))})
	if first := strings.SplitN(Render(m, Theme{}), "\n", 2)[0]; !strings.Contains(first, "token ok") || strings.Contains(first, "rejected") {
		t.Fatalf("header = %q", first)
	}
}

// --- the token in memory ---

func TestEscAtTheTokenPromptWipesWhatWasTyped(t *testing.T) {
	m := newModel().purge()
	m = typeText(m, "mhra_secret")
	held := m.Prompt.Secret
	if string(held) != "mhra_secret" {
		t.Fatalf("secret = %q", held)
	}
	m, _ = press(m, "esc")
	if m.Prompt != nil {
		t.Fatal("esc closes the prompt")
	}
	for i, b := range held[:cap(held)] {
		if b != 0 {
			t.Fatalf("byte %d of the typed token survives", i)
		}
	}
}

func TestReplacingTheTokenPromptWipesIt(t *testing.T) {
	m := newModel().purge()
	m = typeText(m, "mhra_secret")
	held := m.Prompt.Secret
	m = m.purge() // a second prompt replaces the first
	for _, b := range held[:cap(held)] {
		if b != 0 {
			t.Fatal("the replaced prompt kept the token")
		}
	}
}

func TestTypingTheTokenNeverReallocatesTheBuffer(t *testing.T) {
	m := newModel().purge()
	m = typeText(m, "a")
	first := &m.Prompt.Secret[:1][0]
	m = typeText(m, strings.Repeat("b", 100))
	if &m.Prompt.Secret[0] != first {
		t.Fatal("the buffer moved, leaving the first copy behind")
	}
	// Characters beyond the buffer are not collected.
	m = typeText(m, strings.Repeat("c", 5000))
	if len(m.Prompt.Secret) > 1024 {
		t.Fatalf("%d bytes collected", len(m.Prompt.Secret))
	}
	if &m.Prompt.Secret[0] != first {
		t.Fatal("the buffer moved when it filled")
	}
}

func TestSubmittedTokenIsCopiedAndTheTypedBufferWiped(t *testing.T) {
	m := newModel().purge()
	m = typeText(m, "tok")
	held := m.Prompt.Secret
	m, effs := press(m, "enter")
	c, ok := find[CheckToken](effs)
	if !ok || string(c.Token) != "tok" {
		t.Fatalf("effects %+v", effs)
	}
	for _, b := range held[:cap(held)] {
		if b != 0 {
			t.Fatal("typed buffer not wiped on submit")
		}
	}
	_ = m
}

func TestAKeyReadIsWipedOnceHandedOver(t *testing.T) {
	src := strings.NewReader("mhra_pasted")
	buf := make([]byte, 64)
	var got []byte
	pump(src, buf, func(b []byte) bool { got = append(got, b...); wipeBytes(b); return true })
	if len(got) == 0 {
		t.Fatal("nothing read")
	}
	for _, b := range buf {
		if b != 0 {
			t.Fatal("the read buffer still holds what was typed")
		}
	}
}

// --- fetches are not stacked ---

func TestAFrameBurstKeepsOneFetchOfEachKindInFlight(t *testing.T) {
	m := newModel()
	m, effs := Init(m)
	if count[FetchList](effs) != 1 {
		t.Fatalf("init: %+v", effs)
	}
	var all []Effect
	for i := 0; i < 20; i++ {
		var e []Effect
		m, e = Update(m, FrameEvent{Frame: api.Frame{Type: "update", RequestID: "x"}})
		all = append(all, e...)
	}
	if count[FetchList](all) != 0 || count[FetchWhitelist](all) != 0 {
		t.Fatalf("a fetch is already on its way: %+v", all)
	}
	// The answer arrives; one follow-up fetch picks up whatever changed since.
	m, effs = Update(m, ListLoaded{Seq: m.ListSeq, Listing: listing()})
	if count[FetchList](effs) != 1 {
		t.Fatalf("one follow-up list fetch: %+v", effs)
	}
	m, effs = Update(m, WhitelistLoaded{Seq: m.WLSeq})
	if count[FetchWhitelist](effs) != 1 {
		t.Fatalf("one follow-up whitelist fetch: %+v", effs)
	}
	// With nothing more asked in between, the next answer ends it.
	m, effs = Update(m, ListLoaded{Seq: m.ListSeq, Listing: listing()})
	if count[FetchList](effs) != 0 {
		t.Fatalf("no endless refetching: %+v", effs)
	}
}

func TestAFetchThatNeverAnswersDoesNotBlockLaterOnes(t *testing.T) {
	m, _ := Init(newModel())
	m.Now = m.Now.Add(fetchPatience + time.Second)
	_, effs := Update(m, FrameEvent{Frame: api.Frame{Type: "update"}})
	if count[FetchList](effs) != 1 {
		t.Fatalf("%+v", effs)
	}
}

func TestAnActionRefetchIsNotLostWhileAFetchIsInFlight(t *testing.T) {
	m, _ := Init(newModel())
	m, _ = Update(m, ListLoaded{Seq: m.ListSeq, Listing: listing(mk("a", api.StatusPending, 1))})
	m, effs := Update(m, FrameEvent{Frame: api.Frame{Type: "update"}})
	if count[FetchList](effs) != 1 {
		t.Fatalf("%+v", effs)
	}
	m, effs = Update(m, ActionDone{Op: OpApprove, ID: "a"})
	if count[FetchList](effs) != 0 {
		t.Fatalf("coalesced: %+v", effs)
	}
	// The answer to the earlier fetch may predate the approve, so another follows.
	_, effs = Update(m, ListLoaded{Seq: m.ListSeq, Listing: listing(mk("a", api.StatusPending, 1))})
	if count[FetchList](effs) != 1 {
		t.Fatalf("the refetch after the action still happens: %+v", effs)
	}
}

func TestAFailedFetchEndsTheInFlightState(t *testing.T) {
	m, _ := Init(newModel())
	m, _ = Update(m, ListLoaded{Seq: m.ListSeq, Err: errors.New("boom")})
	_, effs := Update(m, FrameEvent{Frame: api.Frame{Type: "update"}})
	if count[FetchList](effs) != 1 {
		t.Fatalf("%+v", effs)
	}
}
