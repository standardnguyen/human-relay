package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/standardnguyen/human-relay/tui/api"
	"github.com/standardnguyen/human-relay/tui/safety"
)

const (
	minWidth  = 40
	minHeight = 8
)

var (
	styCmd    = FGGreen
	styMuted  = Dim
	styHead   = Bold
	styErr    = FGRed | Bold
	styWarn   = FGYellow | Bold
	styLabel  = FGCyan
	styCtl    = FGMagenta | Underline
	styNonASC = FGYellow | Reverse
)

func statusStyle(s string) Style {
	switch s {
	case api.StatusPending:
		return FGYellow | Bold
	case api.StatusApproved, api.StatusRunning:
		return FGBlue | Bold
	case api.StatusComplete:
		return FGGreen
	case api.StatusDenied:
		return FGRed
	case api.StatusError:
		return FGRed | Bold
	}
	return Dim
}

// --- building lines: every agent-supplied string goes through txt ---

// txt sanitises s and returns it as spans: control characters in magenta
// escapes, non-ASCII characters highlighted, everything else in base.
func (m Model) txt(s string, base Style) Line {
	var out Line
	for _, sg := range safety.Sanitize(s, safety.Options{ASCII: m.Opts.ASCII}) {
		switch sg.Flag {
		case safety.FlagControl:
			out = append(out, Span{Text: sg.Text, Style: styCtl})
		case safety.FlagNonASCII:
			out = append(out, Span{Text: sg.Text, Style: styNonASC})
		default:
			out = append(out, Span{Text: sg.Text, Style: base})
		}
	}
	return out
}

// block sanitises multi-line text into one Line per source line. It never
// cuts anything: what is shown for review is the whole text.
func (m Model) block(s string, base Style, indent string) []Line {
	lines := safety.Lines(s, safety.Options{ASCII: m.Opts.ASCII})
	out := make([]Line, 0, len(lines))
	for _, segs := range lines {
		l := make(Line, 0, len(segs)+1)
		l = append(l, Span{Text: indent})
		for _, sg := range segs {
			l = append(l, Span{Text: sg.Text, Style: styleFor(sg.Flag, base)})
		}
		out = append(out, l)
	}
	return out
}

func styleFor(f safety.Flag, base Style) Style {
	switch f {
	case safety.FlagControl:
		return styCtl
	case safety.FlagNonASCII:
		return styNonASC
	}
	return base
}

func lit(s string, st Style) Span { return Span{Text: s, Style: st} }

func join(parts ...Line) Line {
	var out Line
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func (m Model) dash() string {
	if m.Opts.ASCII {
		return "-"
	}
	return "—"
}

func (m Model) when(t time.Time) string {
	loc := m.Opts.Loc
	if loc == nil {
		loc = time.Local
	}
	return t.In(loc).Format("2006-01-02 15:04:05 MST")
}

func (m Model) shortWhen(t time.Time) string {
	loc := m.Opts.Loc
	if loc == nil {
		loc = time.Local
	}
	return t.In(loc).Format("01-02 15:04")
}

// --- the whole screen ---

// Render draws the model as a screen of exactly Height lines, none wider than
// Width cells. The theme only decides which escape sequences the style spans
// become; all text in them has been sanitised.
func Render(m Model, th Theme) string {
	if m.Opts.ASCII {
		th = Theme{} // --ascii means no colour and no attributes
	}
	w, h := m.Width, m.Height
	if w < minWidth || h < minHeight {
		lines := make([]string, h)
		msg := fmt.Sprintf("terminal too small (need %dx%d)", minWidth, minHeight)
		for i := range lines {
			lines[i] = ""
		}
		if h > 0 {
			lines[0] = clip(Line{{Text: msg}}, w, true).Plain()
		}
		return strings.Join(lines, "\n")
	}
	var rows []Line
	rows = append(rows, m.headerLine(), m.secondLine())
	body := m.body()
	if m.Prompt != nil {
		body = m.overlayPrompt(body)
	}
	rows = append(rows, body...)
	rows = append(rows, m.statusLine(), m.hintLine())

	out := make([]string, len(rows))
	for i, l := range rows {
		l = clip(l, w, m.Opts.ASCII)
		out[i] = th.render(l)
		// pad lines so the previous frame never shows through
		_ = i
	}
	return strings.Join(out, "\n")
}

func (m Model) headerLine() Line {
	l := Line{lit(" human-relay-tui ", styHead)}
	l = append(l, m.txt(m.Opts.URL, styMuted)...)
	l = append(l, lit("  ", 0))
	switch m.Conn {
	case ConnUp:
		l = append(l, lit("Connected", FGGreen|Bold))
	case ConnDown:
		l = append(l, lit("Disconnected "+m.dash()+" retrying...", FGRed|Bold))
	default:
		l = append(l, lit("Connecting...", styMuted))
	}
	if m.FetchErr != nil {
		l = append(l, lit(" (fetch failed)", styErr))
	}
	l = append(l, lit("  ", 0))
	if n := m.PendingCount(); n > 0 {
		l = append(l, lit(fmt.Sprintf("%d pending", n), FGYellow|Bold))
	} else {
		l = append(l, lit(fmt.Sprintf("%d total", m.Total()), 0))
	}
	if left := m.TurboLeft(); m.TurboActive {
		s := ceilSeconds(left)
		l = append(l, lit(fmt.Sprintf("  Turbo ON %d:%02d", s/60, s%60), FGYellow|Bold|Reverse))
	}
	if left := m.CooldownLeft(); left > 0 {
		l = append(l, lit(fmt.Sprintf("  cooldown %ds", ceilSeconds(left)), FGYellow))
	}
	if m.TokenRejected() {
		l = append(l, lit("  token rejected (401)", styErr))
	} else if m.TokenNote != "" {
		l = append(l, lit("  ", 0))
		l = append(l, m.txt(m.TokenNote, styMuted)...)
	} else if !m.HaveToken {
		l = append(l, lit("  no token", styErr))
	}
	return l
}

func (m Model) secondLine() Line {
	switch m.Mode {
	case ModeDetail:
		if r, ok := m.Selected(); ok {
			return join(Line{lit(" request ", styMuted)}, m.txt(r.ID, styHead))
		}
	case ModeHelp:
		return Line{lit(" help", styHead)}
	}
	bar := func(digits bool) Line {
		l := Line{lit(" ", 0)}
		for f := Filter(0); f < numFilters; f++ {
			name := filterNames[f]
			if f == FilterWhitelist {
				l = append(l, lit("| ", styMuted))
				if len(m.Rules) > 0 {
					name = fmt.Sprintf("%s (%d)", name, len(m.Rules))
				}
			}
			st := Style(0)
			if f == m.Filter {
				st = Reverse | Bold
			}
			if digits {
				l = append(l, lit(fmt.Sprintf("%d:", f), styMuted))
			}
			l = append(l, lit(name, st), lit(" ", 0))
		}
		return l
	}
	for _, digits := range []bool{true, false} {
		if l := bar(digits); l.Width() <= m.Width {
			return l
		}
	}
	name := filterNames[m.Filter]
	if m.Filter == FilterWhitelist && len(m.Rules) > 0 {
		name = fmt.Sprintf("%s (%d)", name, len(m.Rules))
	}
	return Line{lit(" filter: ", styMuted), lit(name, Reverse|Bold), lit("  (0-9, [ ] or tab to change)", styMuted)}
}

func (m Model) statusLine() Line {
	if m.Status.Text == "" {
		return nil
	}
	st := Style(FGGreen)
	if m.Status.Err {
		st = styErr
	}
	return join(Line{lit(" ", 0)}, m.txt(m.Status.Text, st))
}

func (m Model) hintLine() Line {
	// droppable hints come first; the tail is always kept.
	var drop, keep []string
	switch {
	case m.Prompt != nil:
		switch m.Prompt.Kind {
		case PromptDeny, PromptToken:
			keep = []string{"Enter submit", "Esc cancel", "ctrl-u clear"}
		default:
			keep = []string{"y yes", "n no"}
		}
	case m.Mode == ModeHelp:
		keep = []string{"j/k scroll", "esc close"}
	case m.Mode == ModeDetail:
		keep = []string{"esc back"}
		drop = append(append([]string{"j/k scroll"}, m.actionHints()...), "] next", "[ prev")
		keep = append(keep, "? help", "q quit")
	case m.Filter == FilterWhitelist:
		drop = []string{"j/k move", "0-9 filter", "t turbo"}
		keep = []string{"x remove", "? help", "q quit"}
	default:
		drop = append([]string{"j/k move", "enter open"}, m.actionHints()...)
		drop = append(drop, "0-9 filter", "t turbo")
		keep = []string{"? help", "q quit"}
	}
	for {
		all := append(append([]string(nil), drop...), keep...)
		txt := " " + strings.Join(all, "  ")
		if textWidth(txt) <= m.Width || len(drop) == 0 {
			return Line{lit(txt, styMuted)}
		}
		// drop the least important: the generic ones at the tail of drop first
		drop = dropOne(drop)
	}
}

// dropOne removes the least important droppable hint: navigation extras
// first, then from the end, never the first action hint.
func dropOne(h []string) []string {
	for _, low := range []string{"0-9 filter", "t turbo", "j/k move", "j/k scroll", "] next", "[ prev", "enter open"} {
		for i, s := range h {
			if s == low {
				return append(h[:i:i], h[i+1:]...)
			}
		}
	}
	return h[:len(h)-1]
}

func (m Model) actionHints() []string {
	r, ok := m.Selected()
	if !ok {
		return nil
	}
	var h []string
	switch r.Status {
	case api.StatusPending:
		// During a cooldown the approve keys only say how long is left, so they
		// are not offered; deny stays.
		if m.CooldownLeft() == 0 {
			h = append(h, "a approve", "g gated")
			if _, _, ok := api.WhitelistKey(r); ok {
				h = append(h, "A approve+whitelist")
			}
		}
		h = append(h, "d deny")
	case api.StatusWithdrawn:
		h = append(h, "m mark read")
		if _, _, ok := api.WhitelistKey(r); ok && !m.IsWhitelisted(r) {
			h = append(h, "w whitelist")
		}
	default:
		if _, _, ok := api.WhitelistKey(r); ok && !m.IsWhitelisted(r) {
			h = append(h, "w whitelist")
		}
	}
	if r.OutputGated && (r.Status == api.StatusComplete || r.Status == api.StatusError || r.Status == api.StatusRunning) {
		h = append(h, "r release")
	}
	if m.Mode == ModeDetail && ((r.Type != api.TypeHTTP && r.StdinLen > 0) || (r.Type == api.TypeHTTP && m.bodyHasMore(r))) {
		h = append(h, "f full")
	}
	return h
}

// body is the main area, exactly bodyRows lines.
func (m Model) body() []Line {
	rows := m.bodyRows()
	var lines []Line
	switch m.Mode {
	case ModeHelp:
		lines = m.helpLines()
		if m.DetailTop > 0 && m.DetailTop < len(lines) {
			lines = lines[m.DetailTop:]
		}
	case ModeDetail:
		if r, ok := m.Selected(); ok {
			all := DetailLines(m, r, m.Width)
			view := detailView(len(all), rows)
			top := m.DetailTop
			if mx := len(all) - view; top > mx {
				top = mx
			}
			if top < 0 {
				top = 0
			}
			end := top + view
			if end > len(all) {
				end = len(all)
			}
			lines = append(lines, all[top:end]...)
			if len(all) > rows {
				// the last row says where we are; it never covers content
				lines = append(lines, Line{lit(fmt.Sprintf(" [lines %d-%d of %d]", top+1, end, len(all)), styMuted)})
			}
		} else {
			lines = []Line{{lit(" request not found", styErr)}}
		}
	default:
		if m.Filter == FilterWhitelist {
			lines = m.whitelistLines()
		} else {
			lines = m.listLines()
		}
	}
	for len(lines) < rows {
		lines = append(lines, nil)
	}
	return lines[:rows]
}

func (m Model) listLines() []Line {
	shown := m.Shown()
	all := m.Visible()
	hdr := Line{lit("   ID                STATUS     CREATED     REQUEST", styMuted)}
	if len(all) == 0 {
		if m.TokenRejected() {
			return []Line{hdr, {lit(" The relay does not accept this token, so requests cannot be listed.", styErr)},
				{lit(" Press P to enter another token.", styMuted)}}
		}
		return []Line{hdr, {lit(" No requests", styMuted)}}
	}
	rows := m.listRows()
	extra := 0
	if len(all) > DisplayLimit {
		extra = 1
	}
	total := len(shown) + extra
	top := m.effectiveTop(rows, total)
	lines := []Line{hdr}
	for i := top; i < total && len(lines) <= rows; i++ {
		if i >= len(shown) {
			note := fmt.Sprintf(" Showing %d of %d %s s to show all", DisplayLimit, len(all), m.dash())
			if m.ShowAll {
				note = fmt.Sprintf(" Showing all %d %s s to collapse", len(all), m.dash())
			}
			lines = append(lines, Line{lit(note, styMuted)})
			continue
		}
		lines = append(lines, m.listRow(shown[i], i == m.SelIdx))
	}
	return lines
}

func (m Model) listRow(r api.Request, selected bool) Line {
	mark := "  "
	base := Style(0)
	if selected {
		mark = "> "
		base = Bold
	}
	l := Line{lit(mark, FGYellow|Bold)}
	id := m.txt(r.ID, base)
	if w := id.Width(); w < 16 {
		id = append(id, lit(strings.Repeat(" ", 16-w), 0))
	}
	l = append(l, id...)
	l = append(l, lit(" ", 0))
	status := r.Status
	for len(status) < 9 {
		status += " "
	}
	l = append(l, Span{Text: "", Style: 0})
	l = append(l, m.txt(status, statusStyle(r.Status)|base)...)
	l = append(l, lit(" "+m.shortWhen(r.CreatedAt)+"  ", styMuted))
	if r.OutputGated {
		l = append(l, lit("GATED ", styWarn))
	}
	if r.Shell {
		l = append(l, lit("SHELL ", FGRed|Bold))
	}
	if m.requestHasNonASCII(r) {
		l = append(l, lit("NON-ASCII ", styWarn))
	}
	if r.Type != api.TypeHTTP && r.StdinLen > 0 {
		l = append(l, lit("FILE ", FGBlue|Bold))
	}
	l = append(l, m.txt(firstRunes(summaryOf(r), m.Width+1), base)...)
	return l
}

// firstRunes is the first n characters of s. A list row is cut to the screen
// width anyway, and every character takes at least one cell, so the rest of a
// very long command never needs to be examined to draw the row.
func firstRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func (m Model) whitelistLines() []Line {
	hdr := Line{lit("   WHITELIST RULES (auto-approved without review)", styMuted)}
	if len(m.Rules) == 0 {
		return []Line{hdr, {lit(" No whitelisted commands.", styMuted)},
			{lit(" Use w on a finished request to auto-approve it next time.", styMuted)}}
	}
	rows := m.listRows()
	top := m.effectiveTop(rows, len(m.Rules))
	lines := []Line{hdr}
	for i := top; i < len(m.Rules) && len(lines) <= rows; i++ {
		rule := m.Rules[i]
		mark, base := "  ", Style(0)
		if i == m.RuleSel {
			mark, base = "> ", Bold
		}
		l := Line{lit(mark, FGYellow|Bold)}
		l = append(l, m.txt(rule.Command, styCmd|base)...)
		for _, a := range rule.Args {
			l = append(l, lit(" ", 0))
			if a == "" || strings.ContainsAny(a, " \t\n'\"") {
				l = append(l, lit("'", styMuted))
				l = append(l, m.txt(a, base)...)
				l = append(l, lit("'", styMuted))
			} else {
				l = append(l, m.txt(a, base)...)
			}
		}
		lines = append(lines, l)
	}
	return lines
}

func (m Model) helpLines() []Line {
	note := "The token is held in memory only. At startup it is checked by asking the relay to approve a request that cannot exist: " +
		"a 404 means it is accepted for decisions, either as the approver token or, on a relay with no approver token configured (legacy mode), as any valid token; " +
		"a 403 means it is valid but is not the approver token."
	keys := [][2]string{
		{"j k, arrows", "move; scroll inside a request"},
		{"enter, esc", "open a request / go back"},
		{"] [", "in a request: open the next / previous one"},
		{"0-9, tab, [ ]", "filters (9 is the whitelist)"},
		{"s", "show all / show the first 20"},
		{"a, g", "approve / approve gated (output held back until released); from the list the key opens the request first, press it again to approve"},
		{"A", "approve and whitelist (opens the request, then asks; the rule is added only if the approve succeeded)"},
		{"d", "deny, with an optional reason"},
		{"r", "release gated output to the agent"},
		{"w, x", "whitelist a finished request / remove a rule (both ask first)"},
		{"m, C", "mark a withdrawn request read / clear finished requests (until restart)"},
		{"t", "turbocharge on (5 min, 10 s cooldown) / off"},
		{"f e b p H", "in a request: full file or body, whole reason, base64, PowerShell raw, response headers"},
		{"B, R", "toggle the bell / refresh now"},
		{"P", "forget the token and enter another"},
		{"q, ctrl-c", "quit"},
	}
	var out []Line
	out = append(out, wrap(Line{lit(" "+note, styMuted)}, m.Width)...)
	out = append(out, nil)
	for _, k := range keys {
		pad := k[0]
		for len(pad) < 15 {
			pad += " "
		}
		out = append(out, wrap(Line{lit(" "+pad, styLabel), lit(k[1], 0)}, m.Width)...)
	}
	return out
}

func (m Model) overlayPrompt(body []Line) []Line {
	p := m.Prompt
	var box []Line
	box = append(box, Line{lit(strings.Repeat("-", clampInt(m.Width, 1, 200)), styMuted)})
	box = append(box, join(Line{lit(" ", 0)}, m.txt(p.Title, styHead)))
	switch p.Kind {
	case PromptDeny:
		box = append(box, wrapAll(join(Line{lit(" > ", styLabel)}, m.txt(p.Text, 0), Line{lit("_", Reverse)}), m.Width)...)
		box = append(box, Line{lit(" The reason is sent to the agent. Leave it empty to use the relay's default.", styMuted)})
	case PromptToken:
		mask := strings.Repeat("*", len([]rune(string(p.Secret))))
		box = append(box, wrapAll(Line{lit(" > "+mask, styLabel), lit("_", Reverse)}, m.Width)...)
	default:
		for _, l := range m.block(p.Text, 0, " ") {
			box = append(box, wrap(l, m.Width)...)
		}
	}
	rows := len(body)
	if len(box) > rows-1 {
		box = box[len(box)-(rows-1):]
	}
	out := append([]Line(nil), body[:rows-len(box)]...)
	return append(out, box...)
}

func wrapAll(l Line, w int) []Line { return wrap(l, w) }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// --- the detail view ---

// detailView is how many content lines fit when total lines are shown in rows
// rows: all of them if they fit, otherwise one row less for the position marker.
func detailView(total, rows int) int {
	if total > rows {
		return rows - 1
	}
	return rows
}

// DetailLines lays out one request in full, wrapped to width, with every
// field the relay reports. Nothing is clipped: long commands wrap, and the
// caller scrolls.
func DetailLines(m Model, r api.Request, width int) []Line {
	var key layoutKey
	if m.lay != nil {
		key = m.layoutKey(r, width)
		if m.lay.ok && m.lay.key == key {
			return m.lay.lines
		}
	}
	d := &detailBuilder{m: m, r: r}
	d.build()
	out := make([]Line, 0, len(d.lines))
	for _, l := range d.lines {
		out = append(out, wrap(l, width)...)
	}
	if m.lay != nil {
		m.lay.key, m.lay.lines, m.lay.ok = key, out, true
	}
	return out
}

// layoutCache remembers the laid-out detail of the request last drawn, so
// scrolling and the clock tick do not sanitise and wrap megabytes again.
type layoutCache struct {
	key   layoutKey
	lines []Line
	ok    bool

	// Per-request answers that need a pass over every field of a possibly
	// very large request. A request's fields do not change once it is filed,
	// so the answers are kept by id.
	nonASCII map[string]bool
	bodyCut  map[string]bool
}

// remember returns the cached answer for id, or computes and keeps it. The
// cache is dropped when it grows past a few thousand entries.
func remember(c map[string]bool, id string, compute func() bool) (map[string]bool, bool) {
	if v, ok := c[id]; ok {
		return c, v
	}
	if c == nil || len(c) > 4096 {
		c = map[string]bool{}
	}
	v := compute()
	c[id] = v
	return c, v
}

// requestHasNonASCII is safety.RequestHasNonASCII, remembered per request.
func (m Model) requestHasNonASCII(r api.Request) bool {
	if m.lay == nil {
		return safety.RequestHasNonASCII(r)
	}
	var v bool
	m.lay.nonASCII, v = remember(m.lay.nonASCII, r.ID, func() bool { return safety.RequestHasNonASCII(r) })
	return v
}

type layoutKey struct {
	id, status                 string
	width, ver, cooldownSecs   int
	ascii, wl                  bool
	reason, b64, ps, hdr, body bool
	cstate                     ContentStateKind
	cexp                       bool
	clen                       int
}

func (m Model) layoutKey(r api.Request, width int) layoutKey {
	k := layoutKey{id: r.ID, status: r.Status, width: width, ver: m.DetailVer, ascii: m.Opts.ASCII,
		wl: m.IsWhitelisted(r), reason: m.ReasonOpen[r.ID], b64: m.B64Collapsed[r.ID], ps: m.PSRaw[r.ID],
		hdr: m.RespHeaders[r.ID], body: m.BodyFull[r.ID]}
	if r.Status == api.StatusPending {
		k.cooldownSecs = ceilSeconds(m.CooldownLeft())
	}
	if st := m.Content[r.ID]; st != nil {
		k.cstate, k.cexp, k.clen = st.State, st.Expanded, len(st.Body)
	}
	return k
}

type detailBuilder struct {
	m     Model
	r     api.Request
	lines []Line
}

func (d *detailBuilder) add(l Line) { d.lines = append(d.lines, l) }
func (d *detailBuilder) blank()     { d.lines = append(d.lines, nil) }
func (d *detailBuilder) kv(key string, value Line) {
	d.add(join(Line{lit(key+": ", styLabel)}, value))
}
func (d *detailBuilder) kvText(key, value string) { d.kv(key, d.m.txt(value, 0)) }

// multi adds a labelled value that may span several lines.
func (d *detailBuilder) multi(key string, value string, base Style) {
	ls := d.m.block(value, base, "")
	if len(ls) == 0 {
		d.add(Line{lit(key+":", styLabel)})
		return
	}
	if len(ls) == 1 {
		d.kv(key, ls[0])
		return
	}
	d.add(Line{lit(key+":", styLabel)})
	for _, l := range d.m.block(value, base, "  ") {
		d.add(l)
	}
}

func (d *detailBuilder) section(label string, value string, base Style) {
	d.add(Line{lit(label, styLabel)})
	for _, l := range d.m.block(value, base, "  ") {
		d.add(l)
	}
}

func (d *detailBuilder) build() {
	m, r := d.m, d.r
	isHTTP := r.Type == api.TypeHTTP

	// title and badges
	title := join(m.txt(r.ID, styHead), Line{lit("  ", 0)}, m.txt(r.Status, statusStyle(r.Status)))
	if r.OutputGated {
		title = append(title, lit("  GATED", styWarn))
	}
	if !isHTTP && r.Shell {
		title = append(title, lit("  SHELL", FGRed|Bold))
	}
	if !isHTTP && r.StdinLen > 0 {
		title = append(title, lit("  FILE "+safety.FormatSize(r.StdinLen), FGBlue|Bold))
	}
	nonASCII := safety.RequestHasNonASCII(r)
	if nonASCII {
		title = append(title, lit("  NON-ASCII", styWarn))
	}
	if n := safety.CommandLineCount(r); !isHTTP && n > safety.LinesBadgeMin {
		title = append(title, lit(fmt.Sprintf("  %d lines %s scroll to review", n, m.dash()), styWarn))
	}
	if r.Status != api.StatusPending && m.IsWhitelisted(r) {
		title = append(title, lit("  Whitelisted", FGGreen|Bold))
	}
	d.add(title)

	// banners (pending only)
	if r.Status == api.StatusPending {
		if r.Shell {
			d.add(Line{lit(" "+m.shellBanner()+" ", FGRed|Bold|Reverse)})
		}
		if nonASCII {
			d.add(Line{lit(" "+m.nonASCIIBanner()+" ", FGYellow|Bold|Reverse)})
		}
	}
	d.blank()

	// meta
	d.kv("created", Line{lit(m.when(r.CreatedAt), 0)})
	if r.DecidedAt != nil {
		d.kv("decided", Line{lit(m.when(*r.DecidedAt), 0)})
	}
	if r.Client != "" {
		d.kvText("client", r.Client)
	}
	if r.Type != "" {
		d.kvText("type", r.Type)
	}
	if r.WorkingDir != "" {
		d.kvText("cwd", r.WorkingDir)
	}
	if r.Timeout > 0 {
		d.kv("timeout", Line{lit(fmt.Sprintf("%ds", r.Timeout), 0)})
	}
	if r.Status == api.StatusPending && m.CooldownLeft() > 0 {
		d.add(d.cooldownBar())
	}
	if r.Status == api.StatusPending {
		// nothing more: the key hints are in the footer
	}
	d.blank()

	// what will run
	if r.DisplayCommand != "" {
		d.multi("display", r.DisplayCommand, styCmd)
	}
	switch r.Type {
	case api.TypeHTTP:
		d.http()
	case api.TypeScript, api.TypeScriptCreate, api.TypeScriptCreateRun:
		d.script()
	case api.TypeRegistryOp:
		d.registry()
	default:
		d.command()
	}
	if r.StdinSHA256 != "" {
		d.kvText("stdin sha256", r.StdinSHA256)
	}
	d.blank()

	// reason
	d.reason()

	// decoders (not for http)
	if !isHTTP {
		d.decoders()
	}

	// non-ASCII findings
	if nonASCII {
		d.add(Line{lit("non-ASCII characters found in:", styWarn)})
		for _, f := range safety.NonASCIIFindings(r) {
			d.add(join(Line{lit("  "+f.Field+": ", styLabel)}, runeList(f.Runes)))
		}
		d.blank()
	}

	// full file
	if !isHTTP && r.StdinLen > 0 {
		d.content()
	}

	// results
	d.result()
	if r.DenyReason != "" {
		d.add(join(Line{lit("Denied: ", FGRed|Bold)}, m.txt(r.DenyReason, FGRed)))
	}
	if r.WithdrawReason != "" {
		d.add(join(Line{lit("Withdrawn by agent: ", styMuted)}, m.txt(r.WithdrawReason, styMuted)))
	}
}

func runeList(rs []rune) Line {
	var out Line
	for i, r := range rs {
		if i > 0 {
			out = append(out, lit(", ", 0))
		}
		s := fmt.Sprintf("U+%04X", r)
		if sc := safety.ScriptOf(r); sc != "" {
			s += " (" + sc + ")"
		}
		out = append(out, lit(s, styWarn))
	}
	return out
}

func (m Model) shellBanner() string {
	return strings.ReplaceAll(safety.ShellBanner, "—", m.dash())
}

func (m Model) nonASCIIBanner() string {
	return strings.ReplaceAll(safety.NonASCIIBanner, "—", m.dash())
}

func (d *detailBuilder) cooldownBar() Line {
	m := d.m
	left := m.CooldownLeft()
	text := fmt.Sprintf("%ds cooldown", ceilSeconds(left))
	bar := ""
	if m.CooldownDuration > 0 {
		const width = 20
		n := int(int64(width) * int64(left) / int64(m.CooldownDuration))
		n = clampInt(n, 0, width)
		fill, rest := "#", "."
		if !m.Opts.ASCII {
			fill, rest = "█", "░"
		}
		bar = "[" + strings.Repeat(fill, n) + strings.Repeat(rest, width-n) + "] "
	}
	return Line{lit(bar+text, FGYellow)}
}

func (d *detailBuilder) command() {
	m, r := d.m, d.r
	if r.Shell {
		d.kv("shell", Line{lit("yes", FGRed|Bold), lit(" (the command line below is interpreted by sh -c)", styMuted)})
		d.multi("command", safety.CommandText(r), styCmd)
		return
	}
	d.multi("command", r.Command, styCmd)
	for i, a := range r.Args {
		key := fmt.Sprintf("arg %d", i+1)
		if a == "" {
			d.kv(key, Line{lit("(empty)", styMuted)})
			continue
		}
		d.multi(key, a, 0)
	}
	_ = m
}

func (d *detailBuilder) script() {
	r := d.r
	d.kvText("script", r.ScriptName)
	for i, a := range r.ScriptArgs {
		d.multi(fmt.Sprintf("script arg %d", i+1), a, 0)
	}
}

func (d *detailBuilder) registry() {
	r := d.r
	d.kvText("registry op", r.RegistryOp)
	keys := make([]string, 0, len(r.RegistryArgs))
	for k := range r.RegistryArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d.add(join(Line{lit("registry arg: ", styLabel)}, d.m.txt(k, 0), Line{lit("=", styMuted)}, d.m.txt(r.RegistryArgs[k], 0)))
	}
}

func methodStyle(method string) Style {
	switch method {
	case "GET":
		return FGGreen | Bold
	case "POST":
		return FGBlue | Bold
	case "PUT":
		return FGYellow | Bold
	case "PATCH":
		return FGYellow
	case "DELETE":
		return FGRed | Bold
	}
	return Dim
}

func (d *detailBuilder) http() {
	m, r := d.m, d.r
	d.add(join(m.txt(r.HTTPMethod, methodStyle(r.HTTPMethod)), Line{lit(" ", 0)}, m.txt(r.HTTPURL, styCmd)))
	keys := make([]string, 0, len(r.HTTPHeaders))
	for k := range r.HTTPHeaders {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d.add(join(Line{lit("  ", 0)}, m.txt(k, styLabel), Line{lit(": ", 0)}, m.txt(safety.RedactHeader(k, r.HTTPHeaders[k]), 0)))
	}
	if ff := r.HTTPFormFile; ff != nil {
		d.add(join(Line{lit("form file: field=", styLabel)}, m.txt(ff.Field, 0), Line{lit(" filename=", styLabel)}, m.txt(ff.Filename, 0)))
		d.kv("  source", m.txt(ff.Source, 0))
		for i, a := range ff.FetchCmd {
			d.kv(fmt.Sprintf("  fetch cmd arg %d", i+1), m.txt(a, 0))
		}
	}
	fk := make([]string, 0, len(r.HTTPFormFields))
	for k := range r.HTTPFormFields {
		fk = append(fk, k)
	}
	sort.Strings(fk)
	for _, k := range fk {
		d.add(join(Line{lit("form field: ", styLabel)}, m.txt(k, 0), Line{lit("=", styMuted)}, m.txt(r.HTTPFormFields[k], 0)))
	}
	if r.HTTPBody == "" {
		return
	}
	full := m.BodyFull[r.ID]
	var text string
	truncated := false
	if full {
		text = safety.HTTPBodyFull(r.HTTPBody)
	} else {
		text, truncated = safety.HTTPBodyPreview(r.HTTPBody)
	}
	d.add(Line{lit("body:", styLabel)})
	for _, l := range m.block(text, 0, "  ") {
		d.add(l)
	}
	switch {
	case truncated:
		d.add(Line{lit("  ...(truncated)  f show full body", styMuted)})
	case full && safety.TruncateUTF16Cut(r.HTTPBody, safety.HTTPBodyLimit):
		d.add(Line{lit("  f hide full body", styMuted)})
	}
}

func (d *detailBuilder) reason() {
	m, r := d.m, d.r
	if r.Reason == "" {
		return
	}
	preview, cut := safety.ReasonPreview(r.Reason)
	if !cut || m.ReasonOpen[r.ID] {
		d.section("reason:", r.Reason, 0)
		if cut {
			d.add(Line{lit("  e show less", styMuted)})
		}
	} else {
		d.section("reason:", preview, 0)
		d.add(Line{lit("  e show more", styMuted)})
	}
	d.blank()
}

func (d *detailBuilder) decoders() {
	m, r := d.m, d.r
	text := safety.CommandText(r)
	collapsedGlyph, openGlyph := ">", "v"
	if !m.Opts.ASCII {
		collapsedGlyph, openGlyph = "▶", "▼"
	}
	for _, ps := range safety.PowerShellEncoded(text) {
		if !ps.OK {
			d.add(Line{lit("PowerShell -EncodedCommand [could not decode]:", styWarn)})
			for _, l := range m.block(ps.Base64, 0, "  ") {
				d.add(l)
			}
			continue
		}
		d.add(Line{lit("Decoded command (PowerShell -EncodedCommand):", FGGreen|Bold)})
		for _, l := range m.block(ps.Decoded, styCmd, "  ") {
			d.add(l)
		}
		if m.PSRaw[r.ID] {
			d.add(Line{lit(openGlyph+" raw base64  p hide raw base64", styMuted)})
			for _, l := range m.block(ps.Base64, 0, "  ") {
				d.add(l)
			}
		} else {
			d.add(Line{lit(collapsedGlyph+" raw base64  p show raw base64", styMuted)})
		}
	}
	blobs := safety.FindBase64Blobs(text)
	for i, b := range blobs {
		label := "Decoded base64"
		if len(blobs) > 1 {
			label += fmt.Sprintf(" #%d", i+1)
		}
		if m.B64Collapsed[r.ID] {
			d.add(Line{lit(collapsedGlyph+" "+label+"  b show", styMuted)})
			continue
		}
		d.add(Line{lit(openGlyph+" "+label+"  b collapse", FGGreen)})
		for _, l := range m.block(b, 0, "  ") {
			d.add(l)
		}
	}
	if len(blobs) > 0 || len(safety.PowerShellEncoded(text)) > 0 {
		d.blank()
	}
}

func (d *detailBuilder) content() {
	m, r := d.m, d.r
	label := "show full file (" + safety.FormatSize(r.StdinLen) + ")"
	st := m.Content[r.ID]
	switch {
	case st == nil || st.State == ContentNone:
		d.add(Line{lit(label+"  f to show", styLabel)})
	case st.State == ContentLoading:
		d.add(Line{lit("loading full file...", styMuted)})
	case st.State == ContentFailed:
		d.add(join(Line{lit("[failed to load content: ", styErr)}, m.txt(st.Err, styErr), Line{lit("]  f to retry", styErr)}))
	case !st.Expanded:
		d.add(Line{lit(label+"  f to show", styLabel)})
	default:
		text := string(st.Body)
		d.add(Line{lit(fmt.Sprintf("hide full file (%d bytes, %d lines)  f", r.StdinLen, safety.CountLines(text)), styLabel)})
		sum := sha256.Sum256(st.Body)
		actual := hex.EncodeToString(sum[:])
		if r.StdinSHA256 != "" && actual != r.StdinSHA256 {
			d.add(Line{lit("content differs from the listed stdin sha256", styErr)})
		}
		if st.SHA256 != "" && actual != st.SHA256 {
			d.add(Line{lit("content differs from the hash the relay sent with it", styErr)})
		}
		if rs := safety.NonASCIIRunes(text); len(rs) > 0 {
			d.add(join(Line{lit("non-ASCII in content: ", styWarn)}, runeList(rs)))
		}
		for _, l := range m.block(text, 0, "  ") {
			d.add(l)
		}
	}
	d.blank()
}

func (d *detailBuilder) result() {
	m, r := d.m, d.r
	res := r.Result
	if res == nil {
		return
	}
	if r.Type == api.TypeHTTP && res.StatusCode != 0 {
		st := FGYellow | Bold
		switch {
		case res.StatusCode >= 200 && res.StatusCode < 300:
			st = FGGreen | Bold
		case res.StatusCode >= 400:
			st = FGRed | Bold
		}
		d.add(join(Line{lit("response ", styLabel)}, Line{lit(fmt.Sprintf("%d", res.StatusCode), st)}))
		if len(res.ResponseHeaders) > 0 {
			if m.RespHeaders[r.ID] {
				d.add(Line{lit("response headers (H hide)", styMuted)})
				keys := make([]string, 0, len(res.ResponseHeaders))
				for k := range res.ResponseHeaders {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					d.add(join(Line{lit("  ", 0)}, m.txt(k, styLabel), Line{lit(": ", 0)}, m.txt(res.ResponseHeaders[k], 0)))
				}
			} else {
				d.add(Line{lit("response headers (H show)", styMuted)})
			}
		}
		if res.Stdout != "" {
			for _, l := range m.block(safety.PrettyJSONOrRaw(res.Stdout), 0, "  ") {
				d.add(l)
			}
		}
		if res.Stderr != "" {
			d.add(Line{lit("error", styLabel)})
			for _, l := range m.block(res.Stderr, 0, "  ") {
				d.add(l)
			}
		}
		return
	}
	if res.Stdout != "" {
		d.add(Line{lit(fmt.Sprintf("stdout (exit %d)", res.ExitCode), styLabel)})
		for _, l := range m.block(res.Stdout, 0, "  ") {
			d.add(l)
		}
	}
	if res.Stderr != "" {
		d.add(Line{lit("stderr", styLabel)})
		for _, l := range m.block(res.Stderr, 0, "  ") {
			d.add(l)
		}
	}
	if res.Stdout == "" && res.Stderr == "" {
		d.add(Line{lit(fmt.Sprintf("exit code: %d (no output)", res.ExitCode), styLabel)})
	}
}
