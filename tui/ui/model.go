// Package ui is the terminal approval screen: a pure state machine (Update),
// a pure renderer (Render) and a thin terminal loop around them. Every string
// that comes from an agent or from command output reaches the screen through
// the one sanitizer in package safety.
package ui

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/standardnguyen/human-relay/tui/api"
	"github.com/standardnguyen/human-relay/tui/safety"
)

// Timing.
const (
	// PollEvery is how often the lists are refetched on top of the event
	// stream, so a missed frame costs at most one interval.
	PollEvery = 15 * time.Second
	// DisplayLimit is how many requests the list shows before "show all".
	DisplayLimit = 20
	// selectionGuard is how long decision keys are refused after the selection
	// moved by itself (the list was refetched and the selected request left).
	selectionGuard = 800 * time.Millisecond
	// fetchPatience is how long a list fetch counts as still on its way; after
	// that a new one may be sent even if the answer never came.
	fetchPatience = 15 * time.Second
	// resultHold is how long the message about the operator's own action stays
	// in place when a new-request announcement would otherwise replace it.
	resultHold = 5 * time.Second
	statusTTL  = 8 * time.Second
	errorTTL   = 14 * time.Second
)

// Filter is the list filter: one per request status, plus the whitelist view.
type Filter int

// Filters, in the order the 0..9 keys and Tab step through them.
const (
	FilterAll Filter = iota
	FilterPending
	FilterRunning
	FilterComplete
	FilterDenied
	FilterWithdrawn
	FilterError
	FilterApproved
	FilterTimeout
	FilterWhitelist
	numFilters
)

var filterNames = [numFilters]string{"All", "pending", "running", "complete", "denied", "withdrawn", "error", "approved", "timeout", "Whitelist"}

// Status is the request status a filter selects ("" for all).
func (f Filter) Status() string {
	switch f {
	case FilterAll, FilterWhitelist:
		return ""
	}
	return filterNames[f]
}

// Mode is which screen is showing.
type Mode int

// Modes.
const (
	ModeList Mode = iota
	ModeDetail
	ModeHelp
)

// ConnState is the state of the event stream.
type ConnState int

// Connection states.
const (
	ConnConnecting ConnState = iota
	ConnUp
	ConnDown
)

// StatusLine is the message line above the key hints.
type StatusLine struct {
	Text string
	Err  bool
	At   time.Time
	// Result marks the message that reports the outcome of the operator's own
	// action; a new-request announcement does not replace it right away.
	Result bool
}

// PromptKind says which dialog is open.
type PromptKind int

// Prompt kinds.
const (
	PromptDeny PromptKind = iota + 1
	PromptApproveWhitelist
	PromptWhitelistAdd
	PromptRemoveRule
	PromptReauth
	PromptToken
)

// Prompt is an open dialog: a text input (deny reason, token) or a yes/no
// confirmation. Target is the request the dialog was opened for, so the answer
// applies to that request even if the selection has moved since.
type Prompt struct {
	Kind   PromptKind
	Title  string
	Text   string // typed text (deny reason) or the confirmation body
	Target string
	Rule   api.Rule
	Secret []byte // the token being typed; never shown
}

// secretCap is the most bytes the token prompt takes. The buffer is allocated
// once at this size, so typing never leaves an older copy behind.
const secretCap = 1024

// newTokenPrompt opens the hidden token prompt.
func newTokenPrompt() *Prompt {
	return &Prompt{Kind: PromptToken, Title: "Enter the approver token (typing is hidden)", Secret: make([]byte, 0, secretCap)}
}

// wipe zeroes whatever token text the prompt holds.
func (p *Prompt) wipe() {
	if p == nil {
		return
	}
	wipeBytes(p.Secret[:cap(p.Secret)])
	p.Secret = p.Secret[:0]
}

// ContentStateKind is the progress of a full-content fetch.
type ContentStateKind int

// Content states.
const (
	ContentNone ContentStateKind = iota
	ContentLoading
	ContentReady
	ContentFailed
)

// ContentState is the cached stdin of one request.
type ContentState struct {
	State    ContentStateKind
	Body     []byte
	SHA256   string
	Err      string
	Expanded bool
}

// Options are the fixed settings of a run.
type Options struct {
	URL       string
	ASCII     bool
	Bell      bool
	TokenNote string
	Loc       *time.Location
}

// Model is the whole UI state. Update never mutates the Model it is given.
type Model struct {
	Opts          Options
	Now           time.Time
	Width, Height int

	Mode     Mode
	HelpFrom Mode
	Filter   Filter
	ShowAll  bool
	Quit     bool

	Requests []api.Request
	Rules    []api.Rule
	Hidden   map[string]bool

	Sel          string
	SelIdx       int
	SelLockUntil time.Time
	RuleSel      int
	ListTop      int
	DetailTop    int

	Conn      ConnState
	FetchErr  error
	HaveToken bool
	TokenNote string

	CooldownUntil    time.Time
	CooldownDuration time.Duration
	TurboActive      bool
	TurboUntil       time.Time

	Bell        bool
	prevPending map[string]bool
	seeded      bool

	Status StatusLine
	Prompt *Prompt

	Content      map[string]*ContentState
	ReasonOpen   map[string]bool
	B64Collapsed map[string]bool
	PSRaw        map[string]bool
	RespHeaders  map[string]bool
	BodyFull     map[string]bool

	ListSeq, ListApplied int
	WLSeq, WLApplied     int
	// ListSentAt and WLSentAt are when the newest fetch was sent. A fetch is in
	// flight while its sequence number is ahead of the applied one; a request
	// that arrives meanwhile sets the dirty flag and is sent when the answer
	// comes in.
	ListSentAt, WLSentAt time.Time
	ListDirty, WLDirty   bool
	NextPoll             time.Time

	// Advance is a request this user just decided; once the list shows it is
	// no longer pending the selection moves on to the next pending request.
	// Pasting is true between the terminal's bracketed-paste markers: pasted
	// text never acts as key presses, except as text in an open prompt.
	Pasting bool

	// DetailVer changes whenever something the detail layout depends on
	// changes; lay memoizes the layout of the open request.
	DetailVer int
	lay       *layoutCache

	Advance      string
	AdvanceIdx   int
	AdvanceUntil time.Time
}

// NewModel returns the starting state: a token has already been validated.
func NewModel(o Options) Model {
	if o.Loc == nil {
		o.Loc = time.Local
	}
	return Model{
		Opts: o, Bell: o.Bell, HaveToken: true, TokenNote: o.TokenNote,
		Width: 80, Height: 24,
		Hidden: map[string]bool{}, prevPending: map[string]bool{}, Content: map[string]*ContentState{},
		ReasonOpen: map[string]bool{}, B64Collapsed: map[string]bool{}, PSRaw: map[string]bool{},
		RespHeaders: map[string]bool{}, BodyFull: map[string]bool{}, lay: &layoutCache{},
	}
}

func copyBools(m map[string]bool) map[string]bool {
	n := make(map[string]bool, len(m))
	for k, v := range m {
		n[k] = v
	}
	return n
}

// clone copies the maps and the open prompt so Update can change them freely.
func (m Model) clone() Model {
	m.Hidden = copyBools(m.Hidden)
	m.prevPending = copyBools(m.prevPending)
	m.ReasonOpen = copyBools(m.ReasonOpen)
	m.B64Collapsed = copyBools(m.B64Collapsed)
	m.PSRaw = copyBools(m.PSRaw)
	m.RespHeaders = copyBools(m.RespHeaders)
	m.BodyFull = copyBools(m.BodyFull)
	c := make(map[string]*ContentState, len(m.Content))
	for k, v := range m.Content {
		cp := *v
		c[k] = &cp
	}
	m.Content = c
	if m.Prompt != nil {
		p := *m.Prompt
		m.Prompt = &p
	}
	return m
}

// PendingCount is the number of pending requests among everything fetched,
// hidden ones included.
func (m Model) PendingCount() int {
	n := 0
	for _, r := range m.Requests {
		if r.Status == api.StatusPending {
			n++
		}
	}
	return n
}

// Total is the number of requests fetched.
func (m Model) Total() int { return len(m.Requests) }

// CooldownLeft is the approval cooldown still running.
func (m Model) CooldownLeft() time.Duration {
	if d := m.CooldownUntil.Sub(m.Now); d > 0 {
		return d
	}
	return 0
}

// TurboLeft is the turbocharge time still running.
func (m Model) TurboLeft() time.Duration {
	if !m.TurboActive {
		return 0
	}
	if d := m.TurboUntil.Sub(m.Now); d > 0 {
		return d
	}
	return 0
}

func newestFirst(f Filter) bool {
	switch f {
	case FilterAll, FilterComplete, FilterDenied, FilterError, FilterWithdrawn, FilterTimeout:
		return true
	}
	return false
}

// Visible is the list for the current filter: filtered, hidden ids removed,
// sorted (pending first and oldest first in the all/pending/running views;
// newest first for the finished-status views).
func (m Model) Visible() []api.Request {
	if m.Filter == FilterWhitelist {
		return nil
	}
	st := m.Filter.Status()
	var out []api.Request
	for _, r := range m.Requests {
		if (st == "" || r.Status == st) && !m.Hidden[r.ID] {
			out = append(out, r)
		}
	}
	f := m.Filter
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if f == FilterAll || f == FilterPending || f == FilterRunning {
			ap, bp := a.Status == api.StatusPending, b.Status == api.StatusPending
			if ap != bp {
				return ap
			}
			if ap && bp {
				return pendingOrder(a, b)
			}
		}
		if newestFirst(f) {
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.ID < b.ID
		}
		return pendingOrder(a, b)
	})
	return out
}

func pendingOrder(a, b api.Request) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// Shown is Visible cut to the display limit unless show-all is on.
func (m Model) Shown() []api.Request {
	v := m.Visible()
	if !m.ShowAll && len(v) > DisplayLimit {
		return v[:DisplayLimit]
	}
	return v
}

// Selected returns the selected request.
func (m Model) Selected() (api.Request, bool) {
	for _, r := range m.Requests {
		if r.ID == m.Sel {
			return r, true
		}
	}
	return api.Request{}, false
}

// IsWhitelisted reports whether a rule matching the request already exists.
func (m Model) IsWhitelisted(r api.Request) bool {
	cmd, args, ok := api.WhitelistKey(r)
	return ok && api.HasRule(m.Rules, cmd, args)
}

// TokenRejected is true while the relay's last answer to a list fetch was 401.
func (m Model) TokenRejected() bool {
	return m.FetchErr != nil && errors.Is(m.FetchErr, api.ErrUnauthorized)
}

// bodyHasMore reports whether f changes what an http request's body shows:
// the preview cuts it, or the whole body is already open.
func (m Model) bodyHasMore(r api.Request) bool {
	if r.HTTPBody == "" {
		return false
	}
	if m.BodyFull[r.ID] {
		return true
	}
	if m.lay == nil {
		_, cut := safety.HTTPBodyPreview(r.HTTPBody)
		return cut
	}
	var cut bool
	m.lay.bodyCut, cut = remember(m.lay.bodyCut, r.ID, func() bool {
		_, c := safety.HTTPBodyPreview(r.HTTPBody)
		return c
	})
	return cut
}

// tooSmall reports a terminal below the smallest size the screens are laid out for.
func (m Model) tooSmall() bool { return m.Width < minWidth || m.Height < minHeight }

// bodyRows is how many screen rows the main area has.
func (m Model) bodyRows() int {
	n := m.Height - 4
	if n < 1 {
		return 1
	}
	return n
}

// listRows is how many request rows fit under the column header.
func (m Model) listRows() int {
	n := m.bodyRows() - 1
	if n < 1 {
		return 1
	}
	return n
}

// effectiveTop is the first visible list row that keeps the selection on screen.
func (m Model) effectiveTop(rows, total int) int {
	top := m.ListTop
	idx := m.SelIdx
	if m.Filter == FilterWhitelist {
		idx = m.RuleSel
	}
	if idx < top {
		top = idx
	}
	if idx >= top+rows {
		top = idx - rows + 1
	}
	if top > total-rows {
		top = total - rows
	}
	if top < 0 {
		top = 0
	}
	return top
}

func summaryOf(r api.Request) string {
	switch {
	case r.Type == api.TypeHTTP:
		return strings.TrimSpace(r.HTTPMethod + " " + r.HTTPURL)
	case r.DisplayCommand != "":
		return r.DisplayCommand
	}
	s := r.Command
	if len(r.Args) > 0 {
		s += " " + strings.Join(r.Args, " ")
	}
	return s
}
