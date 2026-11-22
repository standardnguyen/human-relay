package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/standardnguyen/human-relay/tui/api"
	"github.com/standardnguyen/human-relay/tui/safety"
)

// Event is anything that can happen to the UI.
type Event interface{ isEvent() }

// Effect is something the runtime must do on the model's behalf.
type Effect interface{ isEffect() }

// Events.
type (
	// KeyEvent is a key press.
	KeyEvent struct{ Key Key }
	// ResizeEvent is a new terminal size.
	ResizeEvent struct{ W, H int }
	// TickEvent advances the clock; it fires several times a second.
	TickEvent struct{ Now time.Time }
	// ListLoaded is the answer to a FetchList effect.
	ListLoaded struct {
		Seq     int
		Listing *api.Listing
		Err     error
	}
	// WhitelistLoaded is the answer to a FetchWhitelist effect.
	WhitelistLoaded struct {
		Seq   int
		Rules []api.Rule
		Err   error
	}
	// ConnEvent reports the event stream going up or down.
	ConnEvent struct{ Up bool }
	// FrameEvent is one message from the event stream.
	FrameEvent struct{ Frame api.Frame }
	// ActionDone is the answer to a Do* effect.
	ActionDone struct {
		Op            ActionOp
		ID            string
		Err           error
		ThenWhitelist bool
	}
	// ContentLoaded is the answer to a FetchContent effect.
	ContentLoaded struct {
		ID      string
		Content *api.Content
		Err     error
	}
	// TokenChecked is the answer to a CheckToken effect.
	TokenChecked struct{ Err error }
)

func (KeyEvent) isEvent()        {}
func (ResizeEvent) isEvent()     {}
func (TickEvent) isEvent()       {}
func (ListLoaded) isEvent()      {}
func (WhitelistLoaded) isEvent() {}
func (ConnEvent) isEvent()       {}
func (FrameEvent) isEvent()      {}
func (ActionDone) isEvent()      {}
func (ContentLoaded) isEvent()   {}
func (TokenChecked) isEvent()    {}

// ActionOp names a relay operation.
type ActionOp int

// Operations.
const (
	OpApprove ActionOp = iota
	OpApproveGated
	OpDeny
	OpRelease
	OpWhitelistAdd
	OpWhitelistRemove
	OpTurboOn
	OpTurboOff
)

// Effects.
type (
	// FetchList asks for GET /api/requests.
	FetchList struct{ Seq int }
	// FetchWhitelist asks for GET /api/whitelist.
	FetchWhitelist struct{ Seq int }
	// DoApprove approves; ThenWhitelist asks for the whitelist rule to follow once it succeeded.
	DoApprove struct {
		ID            string
		Gated         bool
		ThenWhitelist bool
	}
	// DoDeny denies with a reason (possibly empty).
	DoDeny struct{ ID, Reason string }
	// DoRelease releases gated output.
	DoRelease struct{ ID string }
	// DoAddWhitelist whitelists the key of a request.
	DoAddWhitelist struct {
		ID           string
		AfterApprove bool // the whitelisting follows an approve that just succeeded
	}
	// DoRemoveWhitelist removes one rule.
	DoRemoveWhitelist struct{ Rule api.Rule }
	// DoTurbo switches turbocharge on or off.
	DoTurbo struct{ On bool }
	// FetchContent fetches the stdin of a request.
	FetchContent struct{ ID string }
	// RingBell rings the terminal bell.
	RingBell struct{}
	// CheckToken installs a new token and validates it.
	CheckToken struct{ Token []byte }
	// PurgeToken forgets the held token.
	PurgeToken struct{}
	// Quit ends the program.
	Quit struct{}
)

func (FetchList) isEffect()         {}
func (FetchWhitelist) isEffect()    {}
func (DoApprove) isEffect()         {}
func (DoDeny) isEffect()            {}
func (DoRelease) isEffect()         {}
func (DoAddWhitelist) isEffect()    {}
func (DoRemoveWhitelist) isEffect() {}
func (DoTurbo) isEffect()           {}
func (FetchContent) isEffect()      {}
func (RingBell) isEffect()          {}
func (CheckToken) isEffect()        {}
func (PurgeToken) isEffect()        {}
func (Quit) isEffect()              {}

// Init returns the effects that load the first screen.
func Init(m Model) (Model, []Effect) {
	m = m.clone()
	return m, m.fetchBoth()
}

// fetchList asks for the request list unless one is already on its way, in
// which case it is remembered and sent when that answer arrives.
func (m *Model) fetchList() []Effect {
	if m.ListSeq > m.ListApplied && m.Now.Sub(m.ListSentAt) < fetchPatience {
		m.ListDirty = true
		return nil
	}
	m.ListDirty = false
	m.ListSeq++
	m.ListSentAt = m.Now
	return []Effect{FetchList{Seq: m.ListSeq}}
}

func (m *Model) fetchWL() []Effect {
	if m.WLSeq > m.WLApplied && m.Now.Sub(m.WLSentAt) < fetchPatience {
		m.WLDirty = true
		return nil
	}
	m.WLDirty = false
	m.WLSeq++
	m.WLSentAt = m.Now
	return []Effect{FetchWhitelist{Seq: m.WLSeq}}
}

func (m *Model) fetchBoth() []Effect {
	if !m.HaveToken {
		return nil
	}
	return append(m.fetchList(), m.fetchWL()...)
}

// setPrompt opens a dialog in place of the current one, wiping any token text
// the old one held.
func (m *Model) setPrompt(p *Prompt) {
	m.Prompt.wipe()
	m.Prompt = p
}

func (m *Model) closePrompt() { m.setPrompt(nil) }

// openDialog opens a dialog the operator asked for with a key. A message left
// from before is cleared so it does not sit under the question.
func (m *Model) openDialog(p *Prompt) {
	m.Status = StatusLine{}
	m.setPrompt(p)
}

func (m *Model) say(text string)  { m.Status = StatusLine{Text: text, At: m.Now} }
func (m *Model) fail(text string) { m.Status = StatusLine{Text: text, Err: true, At: m.Now} }

// Update applies one event. It returns a new Model and the effects to run.
func Update(m Model, ev Event) (Model, []Effect) {
	m = m.clone()
	var effs []Effect
	switch e := ev.(type) {
	case KeyEvent:
		return m.onKey(e.Key)
	case ResizeEvent:
		m.Width, m.Height = e.W, e.H
		m.fixScroll()
	case TickEvent:
		effs = m.onTick(e.Now)
	case ListLoaded:
		effs = m.onList(e)
	case WhitelistLoaded:
		effs = m.onWhitelist(e)
	case ConnEvent:
		if e.Up {
			m.Conn = ConnUp
			effs = m.fetchBoth()
		} else {
			m.Conn = ConnDown
		}
	case FrameEvent:
		effs = m.fetchBoth()
	case ActionDone:
		effs = m.onAction(e)
	case ContentLoaded:
		m.onContent(e)
	case TokenChecked:
		effs = m.onTokenChecked(e)
	}
	return m, effs
}

func (m *Model) onTick(now time.Time) []Effect {
	m.Now = now
	var effs []Effect
	if m.Status.Text != "" {
		ttl := statusTTL
		if m.Status.Err {
			ttl = errorTTL
		}
		if now.Sub(m.Status.At) > ttl {
			m.Status = StatusLine{}
		}
	}
	if m.TurboActive && !now.Before(m.TurboUntil) {
		m.TurboActive = false
		if m.HaveToken {
			effs = append(effs, m.fetchList()...)
		}
	}
	if m.NextPoll.IsZero() {
		m.NextPoll = now.Add(PollEvery)
	} else if !now.Before(m.NextPoll) {
		every := PollEvery
		if m.Conn == ConnDown {
			every = PollEvery / 3
		}
		m.NextPoll = now.Add(every)
		if len(effs) == 0 {
			effs = m.fetchBoth()
		} else {
			effs = append(effs, m.fetchWL()...)
		}
	}
	return effs
}

func (m *Model) onList(e ListLoaded) []Effect {
	effs := m.applyList(e)
	if m.ListDirty && m.ListApplied >= m.ListSeq {
		// something asked for a fetch while this one was on its way
		effs = append(effs, m.fetchList()...)
	}
	return effs
}

func (m *Model) applyList(e ListLoaded) []Effect {
	if e.Seq < m.ListApplied {
		return nil
	}
	m.DetailVer++
	m.ListApplied = e.Seq
	if e.Err != nil {
		m.FetchErr = e.Err
		if errors.Is(e.Err, api.ErrUnauthorized) {
			m.fail("401 unauthorized: the relay does not accept this token (press P to enter another)")
		} else if !m.HaveToken {
			// nothing to say; the token prompt is already up
		} else {
			m.fail("list failed: " + e.Err.Error())
		}
		return nil
	}
	if e.Listing == nil {
		return nil
	}
	m.FetchErr = nil
	l := e.Listing
	m.Requests = l.Requests
	if l.CooldownDuration > 0 {
		m.CooldownDuration = l.CooldownDuration
	}
	if l.CooldownRemaining > 0 {
		m.CooldownUntil = m.Now.Add(l.CooldownRemaining)
	} else {
		m.CooldownUntil = time.Time{}
	}
	m.TurboActive = l.TurboActive
	if l.TurboActive {
		m.TurboUntil = m.Now.Add(l.TurboRemaining)
	}

	live := map[string]bool{}
	pending := map[string]bool{}
	for _, r := range m.Requests {
		live[r.ID] = true
		if r.Status == api.StatusPending {
			pending[r.ID] = true
		}
	}
	for id := range m.Content {
		if !live[id] {
			delete(m.Content, id)
		}
	}
	for _, mp := range []map[string]bool{m.ReasonOpen, m.B64Collapsed, m.PSRaw, m.RespHeaders, m.BodyFull} {
		for id := range mp {
			if !live[id] {
				delete(mp, id)
			}
		}
	}
	var effs []Effect
	var newest *api.Request
	fresh := 0
	for i := range m.Requests {
		r := &m.Requests[i]
		if pending[r.ID] && !m.prevPending[r.ID] {
			fresh++
			if newest == nil || r.CreatedAt.Before(newest.CreatedAt) {
				newest = r
			}
		}
	}
	if m.seeded && newest != nil {
		if m.Bell {
			effs = append(effs, RingBell{})
		}
		// The outcome of the operator's own action stays readable for a few
		// seconds; the new request is in the list and the pending count anyway.
		if !(m.Status.Result && m.Now.Sub(m.Status.At) < resultHold) {
			text := "new request: " + announce(*newest)
			if fresh > 1 {
				text += fmt.Sprintf(" (+%d more)", fresh-1)
			}
			m.say(text)
		}
	}
	m.prevPending, m.seeded = pending, true

	prev := m.Sel
	if m.viewingRequest() && m.openRequestListed() {
		// The request being read stays open however its status changed, even
		// when the filter no longer lists it.
		m.followSel()
		return effs
	}
	m.fixSel()
	m.advanceSelection()
	if prev != "" && m.Sel != prev {
		m.SelLockUntil = m.Now.Add(selectionGuard)
	}
	if m.viewingRequest() {
		if m.Mode == ModeHelp {
			m.HelpFrom = ModeList
		} else {
			m.Mode = ModeList
		}
		m.DetailTop = 0
		m.say("the request you were viewing is no longer listed")
	}
	return effs
}

// viewingRequest is true while a request is open, including while the help
// screen is shown over it.
func (m *Model) viewingRequest() bool {
	return m.Mode == ModeDetail || (m.Mode == ModeHelp && m.HelpFrom == ModeDetail)
}

// openRequestListed is true while the selected request is still known to the
// relay and has not been hidden.
func (m *Model) openRequestListed() bool {
	_, ok := m.Selected()
	return ok && !m.Hidden[m.Sel]
}

// followSel keeps the row index of the selection current without changing which
// request is selected.
func (m *Model) followSel() {
	for i, r := range m.Shown() {
		if r.ID == m.Sel {
			m.SelIdx = i
			break
		}
	}
	m.fixScroll()
}

// leaveDetail goes back to the list. If the request that was open is no longer
// among the rows, the selection moves to one that is, and the decision keys
// wait a moment so a key pressed meanwhile cannot land on that other row.
func (m *Model) leaveDetail() {
	m.Mode, m.DetailTop = ModeList, 0
	prev := m.Sel
	m.fixSel()
	if prev != "" && m.Sel != prev {
		m.SelLockUntil = m.Now.Add(selectionGuard)
	}
}

// advanceSelection moves the selection from a request the user just decided to
// the next pending one, once the list shows the decision.
func (m *Model) advanceSelection() {
	if m.Advance == "" {
		return
	}
	if !m.Now.Before(m.AdvanceUntil) {
		m.Advance = ""
		return
	}
	r, ok := m.byID(m.Advance)
	if ok && r.Status == api.StatusPending {
		return // the decision has not shown up yet
	}
	id := m.Advance
	m.Advance = ""
	if m.Sel != id {
		return
	}
	// The next pending request at or after where the decided one was (the
	// rows below it moved up by one), else the nearest one above.
	shown := m.Shown()
	pick := -1
	for i := m.AdvanceIdx; i < len(shown); i++ {
		if shown[i].Status == api.StatusPending && shown[i].ID != id {
			pick = i
			break
		}
	}
	for i := min(m.AdvanceIdx, len(shown)) - 1; pick < 0 && i >= 0; i-- {
		if shown[i].Status == api.StatusPending && shown[i].ID != id {
			pick = i
		}
	}
	if pick >= 0 {
		m.Sel, m.SelIdx = shown[pick].ID, pick
		m.fixScroll()
	}
}

func (m *Model) onWhitelist(e WhitelistLoaded) []Effect {
	effs := m.applyWhitelist(e)
	if m.WLDirty && m.WLApplied >= m.WLSeq {
		effs = append(effs, m.fetchWL()...)
	}
	return effs
}

func (m *Model) applyWhitelist(e WhitelistLoaded) []Effect {
	if e.Seq < m.WLApplied {
		return nil
	}
	m.DetailVer++
	m.WLApplied = e.Seq
	if e.Err != nil {
		if !errors.Is(e.Err, api.ErrUnauthorized) {
			m.fail("whitelist fetch failed: " + e.Err.Error())
		}
		return nil
	}
	m.Rules = e.Rules
	if m.RuleSel >= len(m.Rules) {
		m.RuleSel = len(m.Rules) - 1
	}
	if m.RuleSel < 0 {
		m.RuleSel = 0
	}
	return nil
}

func (m *Model) onContent(e ContentLoaded) {
	m.DetailVer++
	st := m.Content[e.ID]
	if st == nil {
		return
	}
	if e.Err != nil {
		st.State, st.Err, st.Expanded = ContentFailed, e.Err.Error(), false
		return
	}
	st.State, st.Err, st.Expanded = ContentReady, "", true
	if e.Content != nil {
		st.Body, st.SHA256 = e.Content.Body, e.Content.SHA256
	}
}

func (m *Model) onTokenChecked(e TokenChecked) []Effect {
	if e.Err == nil {
		m.HaveToken = true
		m.closePrompt()
		m.seeded = false
		if m.Opts.TokenNote != "" {
			m.TokenNote = m.Opts.TokenNote
		}
		m.say("token accepted")
		return m.fetchBoth()
	}
	m.HaveToken = false
	m.TokenNote = ""
	switch {
	case errors.Is(e.Err, api.ErrForbidden):
		m.fail("that token is valid but cannot approve (it is not the approver token); enter the approver token")
	case errors.Is(e.Err, api.ErrUnauthorized):
		m.fail("the relay rejected that token (401); enter it again")
	default:
		m.fail("token check failed: " + e.Err.Error())
	}
	m.setPrompt(newTokenPrompt())
	return nil
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

func (m *Model) onAction(e ActionDone) []Effect {
	effs := m.doneAction(e)
	m.Status.Result = m.Status.Text != ""
	return effs
}

func (m *Model) doneAction(e ActionDone) []Effect {
	subject := ""
	if r, ok := m.byID(e.ID); ok {
		subject = " " + clipText(summaryOf(r), 50)
	}
	if e.Err == nil {
		var effs []Effect
		switch e.Op {
		case OpApprove, OpApproveGated, OpDeny:
			if m.Sel == e.ID && !m.viewingRequest() {
				m.Advance, m.AdvanceIdx, m.AdvanceUntil = e.ID, m.SelIdx, m.Now.Add(10*time.Second)
			}
		}
		switch e.Op {
		case OpApprove:
			if e.ThenWhitelist {
				m.say("approved" + subject + "; whitelisting")
				effs = append(effs, DoAddWhitelist{ID: e.ID, AfterApprove: true})
			} else {
				m.say("approved" + subject)
			}
		case OpApproveGated:
			m.say("approved (output gated)" + subject)
		case OpDeny:
			m.say("denied" + subject)
		case OpRelease:
			m.say("output released" + subject)
		case OpWhitelistAdd:
			m.say("whitelisted" + subject)
			effs = append(effs, m.fetchWL()...)
		case OpWhitelistRemove:
			m.say("whitelist rule removed")
			effs = append(effs, m.fetchWL()...)
		case OpTurboOn:
			m.say("turbo on")
		case OpTurboOff:
			m.say("turbo off")
		}
		if e.Op != OpWhitelistRemove {
			effs = append(effs, m.fetchList()...)
		}
		return effs
	}

	var ae *api.Error
	asErr := errors.As(e.Err, &ae)
	name := map[ActionOp]string{OpApprove: "approve", OpApproveGated: "approve (gated)", OpDeny: "deny", OpRelease: "release",
		OpWhitelistAdd: "whitelist", OpWhitelistRemove: "remove rule", OpTurboOn: "turbo on", OpTurboOff: "turbo off"}[e.Op]
	if e.Op == OpWhitelistAdd && e.ThenWhitelist {
		name = "approved, but whitelisting"
	}
	switch {
	case asErr && ae.NotApprover():
		m.fail(name + " refused: " + e.Err.Error())
		m.setPrompt(&Prompt{Kind: PromptReauth, Title: "This token cannot approve",
			Text: "The relay says this token is not its approver token. Clear it and enter the approver token?"})
		return nil
	case errors.Is(e.Err, api.ErrCooldown):
		rem := time.Duration(0)
		if asErr {
			rem = ae.Remaining()
		}
		if rem > 0 {
			m.CooldownUntil = m.Now.Add(rem)
		}
		m.fail(fmt.Sprintf("cooldown active: try again in %ds", ceilSeconds(rem)))
	case errors.Is(e.Err, api.ErrUnauthorized):
		m.fail(name + " failed: " + e.Err.Error() + " (press P to enter another token)")
	default:
		m.fail(name + " failed: " + e.Err.Error())
	}
	return m.fetchList()
}

func (m *Model) byID(id string) (api.Request, bool) {
	for _, r := range m.Requests {
		if r.ID == id {
			return r, true
		}
	}
	return api.Request{}, false
}

// fixSel keeps the selection on a request that is shown. It follows the
// request by id; if that request left, it takes the row now at the same place.
func (m *Model) fixSel() {
	if m.Filter == FilterWhitelist {
		if m.RuleSel >= len(m.Rules) {
			m.RuleSel = len(m.Rules) - 1
		}
		if m.RuleSel < 0 {
			m.RuleSel = 0
		}
		m.fixScroll()
		return
	}
	shown := m.Shown()
	if len(shown) == 0 {
		m.Sel, m.SelIdx = "", 0
		m.fixScroll()
		return
	}
	for i, r := range shown {
		if r.ID == m.Sel {
			m.SelIdx = i
			m.fixScroll()
			return
		}
	}
	i := m.SelIdx
	if m.Sel == "" || i < 0 {
		i = 0
	}
	if i >= len(shown) {
		i = len(shown) - 1
	}
	m.Sel, m.SelIdx = shown[i].ID, i
	m.fixScroll()
}

func (m *Model) fixScroll() {
	total := len(m.Shown())
	if m.Filter == FilterWhitelist {
		total = len(m.Rules)
	}
	m.ListTop = m.effectiveTop(m.listRows(), total)
}

// announce is the one-line description of a new request: what it runs and why,
// cut at 100 characters.
func announce(r api.Request) string {
	title := r.Command
	if r.Type == api.TypeHTTP {
		title = strings.TrimSpace(r.HTTPMethod + " " + r.HTTPURL)
	} else if title == "" {
		title = summaryOf(r)
	}
	s := strings.ReplaceAll(firstRunes(title, 101)+" - "+firstRunes(r.Reason, 101), "\n", " ")
	if rs := []rune(s); len(rs) > 100 {
		s = string(rs[:100]) + "..."
	}
	return s
}

func clipText(s string, n int) string {
	r := []rune(strings.ReplaceAll(firstRunes(s, n+1), "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "~"
}

// --- keys ---

func (m Model) onKey(k Key) (Model, []Effect) {
	if k.Type == KeyCtrlC {
		m.Prompt.wipe()
		m.Quit = true
		return m, []Effect{Quit{}}
	}
	switch k.Type {
	case KeyPasteStart:
		m.Pasting = true
		return m, nil
	case KeyPasteEnd:
		m.Pasting = false
		return m, nil
	}
	if m.tooSmall() {
		// Nothing the screen shows can be acted on, so nothing is: no decision
		// key, no prompt answer, no typing into a prompt. Quitting still works.
		if k.Type == KeyRune && k.Rune == 'q' && !m.Pasting {
			m.Quit = true
			return m, []Effect{Quit{}}
		}
		return m, nil
	}
	if m.Pasting {
		// Pasted text is data: it is typed into a text prompt, and is
		// otherwise ignored, so it can never act as commands.
		if m.Prompt != nil && (m.Prompt.Kind == PromptDeny || m.Prompt.Kind == PromptToken) && k.Type == KeyRune {
			return m.onPromptKey(k)
		}
		return m, nil
	}
	if m.Prompt != nil {
		return m.onPromptKey(k)
	}
	if m.Mode == ModeHelp {
		switch {
		case k.Type == KeyDown, k.Type == KeyRune && k.Rune == 'j':
			m.DetailTop++
		case k.Type == KeyUp, k.Type == KeyRune && k.Rune == 'k':
			if m.DetailTop > 0 {
				m.DetailTop--
			}
		case k.Type == KeyEsc, k.Type == KeyEnter, k.Type == KeyRune && (k.Rune == 'q' || k.Rune == '?'):
			m.Mode, m.DetailTop = m.HelpFrom, 0
		}
		return m, nil
	}
	if k.Type == KeyCtrlL {
		return m, nil
	}
	switch k.Type {
	case KeyEsc, KeyLeft, KeyBackspace:
		if m.Mode == ModeDetail {
			m.leaveDetail()
		}
		return m, nil
	case KeyEnter, KeyRight:
		return m.open()
	case KeyUp:
		return m.move(-1)
	case KeyDown:
		return m.move(1)
	case KeyPgUp:
		return m.move(-m.pageSize())
	case KeyPgDn:
		return m.move(m.pageSize())
	case KeyHome:
		return m.moveTo(0)
	case KeyEnd:
		return m.moveTo(1 << 30)
	case KeyTab:
		if m.Mode == ModeList {
			return m.setFilter((m.Filter + 1) % numFilters), nil
		}
		return m, nil
	case KeyBacktab:
		if m.Mode == ModeList {
			return m.setFilter((m.Filter + numFilters - 1) % numFilters), nil
		}
		return m, nil
	case KeyDelete:
		return m.removeRule()
	case KeyRune:
		return m.onRune(k.Rune)
	}
	return m, nil
}

// stepDetail opens the next (d=1) or previous (d=-1) request among the rows.
func (m Model) stepDetail(d int) Model {
	shown := m.Shown()
	if len(shown) == 0 {
		return m
	}
	idx := -1
	for i, r := range shown {
		if r.ID == m.Sel {
			idx = i
		}
	}
	i := idx + d
	if idx < 0 {
		// The open request is no longer among the rows: the row that took its
		// place is the next one.
		i = m.SelIdx
		if d < 0 {
			i--
		}
	}
	if i >= len(shown) {
		i = len(shown) - 1
	}
	if i < 0 {
		i = 0
	}
	m.Sel, m.SelIdx, m.DetailTop, m.Advance = shown[i].ID, i, 0, ""
	m.fixScroll()
	return m
}

func (m Model) pageSize() int {
	n := m.bodyRows() - 1
	if n < 1 {
		return 1
	}
	return n
}

func (m Model) open() (Model, []Effect) {
	if m.Mode == ModeList && m.Filter != FilterWhitelist {
		if _, ok := m.Selected(); ok {
			m.Mode, m.DetailTop, m.Advance = ModeDetail, 0, ""
		}
	}
	return m, nil
}

func (m Model) setFilter(f Filter) Model {
	m.Filter, m.ShowAll, m.ListTop, m.RuleSel = f, false, 0, 0
	m.Sel, m.SelIdx = "", 0
	m.fixSel()
	return m
}

func (m Model) move(delta int) (Model, []Effect) {
	if m.Mode == ModeDetail {
		return m.scrollTo(m.DetailTop + delta), nil
	}
	if m.Filter == FilterWhitelist {
		return m.moveTo(m.RuleSel + delta)
	}
	return m.moveTo(m.SelIdx + delta)
}

func (m Model) moveTo(i int) (Model, []Effect) {
	if m.Mode == ModeDetail {
		if i >= 1<<29 {
			i = m.detailMax()
		}
		return m.scrollTo(i), nil
	}
	if m.Filter == FilterWhitelist {
		if i >= len(m.Rules) {
			i = len(m.Rules) - 1
		}
		if i < 0 {
			i = 0
		}
		m.RuleSel = i
		m.fixScroll()
		return m, nil
	}
	shown := m.Shown()
	if len(shown) == 0 {
		return m, nil
	}
	if i >= len(shown) {
		i = len(shown) - 1
	}
	if i < 0 {
		i = 0
	}
	m.Sel, m.SelIdx = shown[i].ID, i
	m.fixScroll()
	return m, nil
}

func (m Model) detailMax() int {
	r, ok := m.Selected()
	if !ok {
		return 0
	}
	all := len(DetailLines(m, r, m.Width))
	n := all - detailView(all, m.bodyRows())
	if n < 0 {
		return 0
	}
	return n
}

func (m Model) scrollTo(i int) Model {
	if mx := m.detailMax(); i > mx {
		i = mx
	}
	if i < 0 {
		i = 0
	}
	m.DetailTop = i
	return m
}

// --- letter keys ---

func (m Model) onRune(r rune) (Model, []Effect) {
	if r >= '0' && r <= '9' && m.Mode == ModeList {
		return m.setFilter(Filter(r - '0')), nil
	}
	switch r {
	case 'q':
		m.Quit = true
		return m, []Effect{Quit{}}
	case '?':
		m.HelpFrom, m.Mode = m.Mode, ModeHelp
		return m, nil
	case 'j':
		return m.move(1)
	case 'k':
		return m.move(-1)
	case 'l':
		return m.open()
	case 'h':
		if m.Mode == ModeDetail {
			m.leaveDetail()
		}
		return m, nil
	case ']', '[':
		d := 1
		if r == '[' {
			d = -1
		}
		if m.Mode == ModeList {
			return m.setFilter((m.Filter + Filter(d) + numFilters) % numFilters), nil
		}
		if m.Mode == ModeDetail {
			return m.stepDetail(d), nil
		}
		return m, nil
	case 's':
		if m.Mode == ModeList && m.Filter != FilterWhitelist {
			m.ShowAll = !m.ShowAll
			m.fixSel()
		}
		return m, nil
	case 'C':
		return m.clearHistory(), nil
	case 'B':
		m.Bell = !m.Bell
		if m.Bell {
			m.say("bell on")
		} else {
			m.say("bell off")
		}
		return m, nil
	case 'R':
		if !m.HaveToken {
			return m.needToken(), nil
		}
		m.say("refreshing")
		return m, m.fetchBoth()
	case 'P':
		return m.purge(), []Effect{PurgeToken{}}
	case 't':
		if !m.HaveToken {
			return m.needToken(), nil
		}
		return m, []Effect{DoTurbo{On: !(m.TurboActive && m.TurboLeft() > 0)}}
	case 'x':
		return m.removeRule()
	case 'a', 'g', 'A', 'd', 'r', 'w', 'm':
		return m.requestKey(r)
	case 'f', 'b', 'p', 'e', 'H':
		return m.detailKey(r)
	}
	return m, nil
}

func (m Model) needToken() Model {
	m.fail("no token set: press P to enter one")
	return m
}

func (m Model) purge() Model {
	m.HaveToken, m.TokenNote = false, ""
	m.Requests, m.Rules = nil, nil
	m.Sel, m.SelIdx, m.RuleSel, m.ListTop = "", 0, 0, 0
	m.Mode = ModeList
	m.prevPending, m.seeded = map[string]bool{}, false
	m.CooldownUntil, m.TurboActive = time.Time{}, false
	m.setPrompt(newTokenPrompt())
	m.say("token cleared")
	return m
}

func (m Model) clearHistory() Model {
	n := 0
	for _, r := range m.Requests {
		switch r.Status {
		case api.StatusComplete, api.StatusDenied, api.StatusError, api.StatusTimeout, api.StatusWithdrawn:
			if !m.Hidden[r.ID] {
				m.Hidden[r.ID] = true
				n++
			}
		}
	}
	m.ShowAll = false
	m.Mode = ModeList
	m.fixSel()
	if n == 0 {
		m.say("nothing to clear")
	} else {
		m.say(fmt.Sprintf("hid %d finished requests (until restart)", n))
	}
	return m
}

func (m Model) removeRule() (Model, []Effect) {
	if m.Filter != FilterWhitelist || m.Mode != ModeList {
		return m, nil
	}
	if !m.HaveToken {
		return m.needToken(), nil
	}
	if m.RuleSel < 0 || m.RuleSel >= len(m.Rules) {
		return m, nil
	}
	rule := m.Rules[m.RuleSel]
	m.openDialog(&Prompt{Kind: PromptRemoveRule, Title: "Remove this whitelist rule?", Rule: rule, Text: ruleText(rule)})
	return m, nil
}

func ruleText(r api.Rule) string {
	if len(r.Args) == 0 {
		return r.Command
	}
	return r.Command + " " + strings.Join(r.Args, " ")
}

// requestKey handles the keys that act on the selected request.
func (m Model) requestKey(r rune) (Model, []Effect) {
	if m.Mode == ModeHelp || (m.Mode == ModeList && m.Filter == FilterWhitelist) {
		return m, nil
	}
	req, ok := m.Selected()
	if !ok {
		return m, nil
	}
	if !m.HaveToken {
		return m.needToken(), nil
	}
	if r == 'm' {
		if req.Status != api.StatusWithdrawn {
			m.fail("mark read applies to withdrawn requests only")
			return m, nil
		}
		m.Hidden[req.ID] = true
		m.Mode = ModeList
		m.fixSel()
		return m, nil
	}
	if m.Now.Before(m.SelLockUntil) {
		m.fail("the selection moved when the list refreshed; check the highlighted request and press the key again")
		return m, nil
	}
	pending := req.Status == api.StatusPending
	switch r {
	case 'a', 'g', 'A', 'd':
		if !pending {
			m.fail(fmt.Sprintf("cannot decide: request is not pending (%s)", req.Status))
			return m, nil
		}
	}
	switch r {
	case 'a', 'g', 'A':
		if left := m.CooldownLeft(); left > 0 {
			m.fail(fmt.Sprintf("cooldown active: try again in %ds", ceilSeconds(left)))
			return m, nil
		}
		if _, _, ok := api.WhitelistKey(req); m.Mode == ModeList && (r != 'A' || ok) {
			// A list row is one clipped line. Approving takes the whole request
			// on screen, so the key opens it first.
			m.Mode, m.DetailTop = ModeDetail, 0
			m.say("review the full request, then press " + string(r) + " again to approve")
			return m, nil
		}
	}
	switch r {
	case 'a':
		return m, []Effect{DoApprove{ID: req.ID}}
	case 'g':
		return m, []Effect{DoApprove{ID: req.ID, Gated: true}}
	case 'A':
		if _, _, ok := api.WhitelistKey(req); !ok {
			m.fail("this kind of request cannot be whitelisted")
			return m, nil
		}
		m.openDialog(&Prompt{Kind: PromptApproveWhitelist, Target: req.ID, Title: "Approve and whitelist?",
			Text: whitelistBody(req, "Approve this request and whitelist it for auto-approval? The rule is added only if the approve succeeds.")})
		return m, nil
	case 'd':
		m.openDialog(&Prompt{Kind: PromptDeny, Target: req.ID, Title: "Deny reason (optional)"})
		return m, nil
	case 'r':
		if !req.OutputGated {
			m.fail("output is not gated")
			return m, nil
		}
		switch req.Status {
		case api.StatusComplete, api.StatusError, api.StatusRunning:
		default:
			m.fail(fmt.Sprintf("nothing to release yet (%s)", req.Status))
			return m, nil
		}
		return m, []Effect{DoRelease{ID: req.ID}}
	case 'w':
		if pending {
			m.fail("use A to approve and whitelist a pending request")
			return m, nil
		}
		cmd, args, ok := api.WhitelistKey(req)
		if !ok {
			m.fail("this kind of request cannot be whitelisted")
			return m, nil
		}
		if api.HasRule(m.Rules, cmd, args) {
			m.fail("already whitelisted")
			return m, nil
		}
		m.openDialog(&Prompt{Kind: PromptWhitelistAdd, Target: req.ID, Title: "Whitelist this command for auto-approval?",
			Text: whitelistBody(req, "")})
		return m, nil
	}
	return m, nil
}

func whitelistBody(req api.Request, lead string) string {
	cmd, args, _ := api.WhitelistKey(req)
	var b strings.Builder
	if lead != "" {
		b.WriteString(lead + "\n\n")
	}
	b.WriteString(cmd)
	for _, a := range args {
		b.WriteString(" " + a)
	}
	if req.Type == api.TypeHTTP {
		b.WriteString("\n\nThe rule matches the method and URL only: any body or headers sent to that URL will be auto-approved.")
	}
	return b.String()
}

// detailKey handles the toggles of the detail view.
func (m Model) detailKey(r rune) (Model, []Effect) {
	if m.Mode != ModeDetail {
		return m, nil
	}
	req, ok := m.Selected()
	if !ok {
		return m, nil
	}
	m.DetailVer++
	flip := func(mp map[string]bool) { mp[req.ID] = !mp[req.ID] }
	switch r {
	case 'e':
		flip(m.ReasonOpen)
	case 'b':
		flip(m.B64Collapsed)
	case 'p':
		flip(m.PSRaw)
	case 'H':
		flip(m.RespHeaders)
	case 'f':
		switch {
		case req.Type == api.TypeHTTP && m.bodyHasMore(req):
			flip(m.BodyFull)
		case req.Type != api.TypeHTTP && req.StdinLen > 0:
			st := m.Content[req.ID]
			switch {
			case st == nil || st.State == ContentNone || st.State == ContentFailed:
				if !m.HaveToken {
					return m.needToken(), nil
				}
				m.Content[req.ID] = &ContentState{State: ContentLoading}
				return m, []Effect{FetchContent{ID: req.ID}}
			case st.State == ContentReady:
				st.Expanded = !st.Expanded
			}
		default:
			m.say("nothing more to show for this request")
		}
	}
	m = m.scrollTo(m.DetailTop)
	return m, nil
}

// --- prompts ---

func (m Model) onPromptKey(k Key) (Model, []Effect) {
	p := m.Prompt
	switch p.Kind {
	case PromptDeny:
		switch k.Type {
		case KeyEsc:
			m.closePrompt()
		case KeyEnter:
			m.closePrompt()
			return m, []Effect{DoDeny{ID: p.Target, Reason: p.Text}}
		case KeyBackspace:
			p.Text = dropLastRune(p.Text)
		case KeyCtrlU:
			p.Text = ""
		case KeyRune:
			p.Text += string(k.Rune)
		}
		return m, nil
	case PromptToken:
		switch k.Type {
		case KeyEsc:
			m.closePrompt()
			m.fail("no token set: press P to enter one")
		case KeyEnter:
			if len(p.Secret) == 0 {
				return m, nil
			}
			tok := append([]byte(nil), p.Secret...)
			for i := range p.Secret {
				p.Secret[i] = 0
			}
			m.closePrompt()
			m.say("checking token")
			return m, []Effect{CheckToken{Token: tok}}
		case KeyBackspace:
			if n := len(p.Secret); n > 0 {
				_, w := utf8.DecodeLastRune(p.Secret)
				for i := n - w; i < n; i++ {
					p.Secret[i] = 0
				}
				p.Secret = p.Secret[:n-w]
			}
		case KeyCtrlU:
			for i := range p.Secret {
				p.Secret[i] = 0
			}
			p.Secret = p.Secret[:0]
		case KeyRune:
			// The buffer never grows: a longer token is not collected.
			if n := utf8.RuneLen(k.Rune); n > 0 && len(p.Secret)+n <= cap(p.Secret) {
				p.Secret = utf8.AppendRune(p.Secret, k.Rune)
			}
		}
		return m, nil
	}
	// yes/no confirmations
	switch {
	case k.Type == KeyEsc, k.Type == KeyRune && (k.Rune == 'n' || k.Rune == 'N'):
		m.closePrompt()
		if p.Kind == PromptReauth {
			m.fail("token unchanged; decisions will keep failing until you press P and enter the approver token")
		}
	case k.Type == KeyRune && (k.Rune == 'y' || k.Rune == 'Y'):
		m.closePrompt()
		if !m.HaveToken && p.Kind != PromptReauth {
			return m.needToken(), nil
		}
		switch p.Kind {
		case PromptApproveWhitelist:
			return m, []Effect{DoApprove{ID: p.Target, ThenWhitelist: true}}
		case PromptWhitelistAdd:
			return m, []Effect{DoAddWhitelist{ID: p.Target}}
		case PromptRemoveRule:
			return m, []Effect{DoRemoveWhitelist{Rule: p.Rule}}
		case PromptReauth:
			return m.purge(), []Effect{PurgeToken{}}
		}
	}
	return m, nil
}

func dropLastRune(s string) string {
	if s == "" {
		return s
	}
	_, w := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-w]
}

var _ = safety.FormatSize
