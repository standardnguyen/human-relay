// Package safety holds everything the approver sees that must be trustworthy:
// the single sanitizer every agent-supplied string passes through before it
// reaches the terminal, the non-ASCII and homoglyph checks, and the base64 and
// PowerShell -EncodedCommand decoders.
package safety

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Flag says why a segment of text is more than plain text.
type Flag uint8

const (
	// FlagNone is ordinary text.
	FlagNone Flag = iota
	// FlagControl is a control character or an invalid byte, shown as an
	// escape such as \x1b, \t, \n or <U+009B> instead of being emitted.
	FlagControl
	// FlagNonASCII is a character outside printable ASCII and the typographic
	// punctuation prose uses. It is shown (or, if it is invisible, shown as
	// <U+XXXX>) and meant to be highlighted.
	FlagNonASCII
)

// Seg is a run of already-safe display text. Text never contains a control
// character, a bidi control or an invisible character.
type Seg struct {
	Text string
	Flag Flag
	Rune rune // the character a flagged segment stands for
}

// Options controls the sanitizer.
type Options struct {
	// ASCII shows every non-ASCII character as <U+XXXX> and maps typographic
	// punctuation to ASCII look-alikes, so the output is pure ASCII.
	ASCII bool
}

// asciiFor maps the typographic punctuation the dashboard also treats as
// ordinary prose to ASCII, for --ascii output.
var asciiFor = map[rune]string{
	0x2013: "-", 0x2014: "-", 0x2018: "'", 0x2019: "'", 0x201C: "\"", 0x201D: "\"", 0x2026: "...",
}

// allowedRune reports whether r is part of what counts as plain text: printable
// ASCII, newline, carriage return, tab and the typographic punctuation.
func allowedRune(r rune) bool {
	if r >= 0x20 && r <= 0x7e {
		return true
	}
	switch r {
	case '\n', '\r', '\t', 0x2013, 0x2014, 0x2018, 0x2019, 0x201C, 0x201D, 0x2026:
		return true
	}
	return false
}

func tag(r rune) string { return fmt.Sprintf("<U+%04X>", r) }

// Sanitize turns arbitrary agent-supplied text into display segments. It works
// on bytes, so invalid UTF-8 (including a lone 0x9b) is rendered as \xNN, and
// it never emits a character the terminal could act on: ESC, every other C0
// control, DEL, the C1 range, bidi controls, zero-width and other invisible
// characters, combining marks and private-use characters all become visible
// text. Newline, tab and carriage return are shown as \n, \t and \r.
func Sanitize(s string, o Options) []Seg {
	var segs []Seg
	// run collects consecutive plain characters so a long line costs time
	// proportional to its length.
	var run strings.Builder
	endRun := func() {
		if run.Len() > 0 {
			segs = append(segs, Seg{Text: run.String()})
			run = strings.Builder{}
		}
	}
	flagged := func(t string, f Flag, r rune) {
		endRun()
		segs = append(segs, Seg{Text: t, Flag: f, Rune: r})
	}

	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n <= 1 {
			flagged(fmt.Sprintf("\\x%02x", s[i]), FlagControl, rune(s[i]))
			i++
			continue
		}
		i += n
		switch {
		case r == '\t':
			flagged(`\t`, FlagControl, r)
		case r == '\n':
			flagged(`\n`, FlagControl, r)
		case r == '\r':
			flagged(`\r`, FlagControl, r)
		case r < 0x20 || r == 0x7f:
			flagged(fmt.Sprintf("\\x%02x", r), FlagControl, r)
		case r < 0x7f:
			run.WriteRune(r)
		case r >= 0x80 && r <= 0x9f:
			flagged(tag(r), FlagControl, r)
		case allowedRune(r):
			if o.ASCII {
				run.WriteString(asciiFor[r])
			} else {
				run.WriteRune(r)
			}
		case o.ASCII || unicode.IsMark(r) || !unicode.IsPrint(r):
			flagged(tag(r), FlagNonASCII, r)
		default:
			flagged(string(r), FlagNonASCII, r)
		}
	}
	endRun()
	return segs
}

// Plain is Sanitize joined into one string, for width maths and tests.
func Plain(s string, o Options) string {
	var b strings.Builder
	for _, sg := range Sanitize(s, o) {
		b.WriteString(sg.Text)
	}
	return b.String()
}

// Lines sanitizes multi-line text: it splits on newline (CRLF counts as one
// newline) and sanitizes each line. A carriage return that is not part of a
// CRLF is shown as \r, never emitted.
func Lines(s string, o Options) [][]Seg {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	parts := strings.Split(s, "\n")
	out := make([][]Seg, len(parts))
	for i, p := range parts {
		out[i] = Sanitize(p, o)
	}
	return out
}
