package ui

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Style is a set of display attributes and one foreground colour.
type Style uint16

// Attributes and colours. Colours occupy their own bits so a style is one value.
const (
	Bold Style = 1 << iota
	Dim
	Reverse
	Underline
)

// Foreground colours.
const (
	FGRed Style = (iota + 1) << 8
	FGGreen
	FGYellow
	FGBlue
	FGMagenta
	FGCyan
	FGWhite
)

const fgMask Style = 0x0F00

// Span is a run of text in one style. Text is always already sanitised.
type Span struct {
	Text  string
	Style Style
}

// Line is a sequence of spans.
type Line []Span

// Width is the number of terminal cells the line occupies.
func (l Line) Width() int {
	w := 0
	for _, s := range l {
		w += textWidth(s.Text)
	}
	return w
}

// Plain is the line's text without styling.
func (l Line) Plain() string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Text)
	}
	return b.String()
}

func textWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// runeWidth is the cell width of a printable rune: 2 for East Asian wide and
// emoji ranges, otherwise 1. (Zero-width characters never reach the renderer;
// the sanitizer turns them into visible <U+XXXX> text.)
func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115f,
		r >= 0x25fd && r <= 0x25fe,
		r >= 0xa960 && r <= 0xa97c,   // Hangul Jamo Extended-A
		r >= 0xfe10 && r <= 0xfe19,   // vertical forms
		r >= 0x16fe0 && r <= 0x16ff1, // Tangut and Khitan marks
		r >= 0x17000 && r <= 0x18d08, // Tangut and its components
		r >= 0x1aff0 && r <= 0x1b2ff, // Kana extensions, Nushu
		r >= 0x2e80 && r <= 0x303e,
		r >= 0x3041 && r <= 0xa4cf,
		r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff,
		r >= 0xfe30 && r <= 0xfe6f,
		r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6,
		// Overcounting only leaves a blank cell; undercounting would push the
		// rest of the row off the edge, so symbol and emoji blocks count as wide.
		r >= 0x2300 && r <= 0x23ff,
		r >= 0x2600 && r <= 0x27bf,
		r >= 0x2b00 && r <= 0x2bff,
		r >= 0x1f000 && r <= 0x1ffff,
		r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// wrap breaks a line into lines of at most width cells, keeping span styles.
// It breaks at the width exactly (nothing is dropped) and never splits a wide
// character. An empty line stays one empty line.
func wrap(l Line, width int) []Line {
	if width < 1 {
		width = 1
	}
	var out []Line
	var cur Line
	w := 0
	flush := func() {
		out = append(out, cur)
		cur, w = nil, 0
	}
	for _, sp := range l {
		var buf strings.Builder
		emit := func() {
			if buf.Len() > 0 {
				cur = append(cur, Span{Text: buf.String(), Style: sp.Style})
				buf.Reset()
			}
		}
		for _, r := range sp.Text {
			rw := runeWidth(r)
			if w+rw > width && w > 0 {
				emit()
				flush()
			}
			buf.WriteRune(r)
			w += rw
		}
		emit()
	}
	if len(cur) > 0 || len(out) == 0 {
		flush()
	}
	return out
}

// clip cuts a line to width cells and marks the cut.
func clip(l Line, width int, ascii bool) Line {
	if l.Width() <= width {
		return l
	}
	marker, mw := "…", 1
	if ascii {
		marker, mw = "...", 3
	}
	if width <= mw {
		return Line{{Text: strings.Repeat(".", width)}}
	}
	keep := width - mw
	var out Line
	w := 0
	for _, sp := range l {
		var b strings.Builder
		for _, r := range sp.Text {
			rw := runeWidth(r)
			if w+rw > keep {
				if b.Len() > 0 {
					out = append(out, Span{Text: b.String(), Style: sp.Style})
				}
				return append(out, Span{Text: marker, Style: Dim})
			}
			b.WriteRune(r)
			w += rw
		}
		out = append(out, Span{Text: b.String(), Style: sp.Style})
	}
	return out
}

// Theme decides which escape sequences the renderer may emit. The zero Theme
// emits none at all.
type Theme struct {
	Color bool // colours and attributes
	Attrs bool // attributes only (bold, dim, reverse, underline)
}

func (t Theme) sgr(s Style) string {
	var codes []string
	if t.Color || t.Attrs {
		if s&Bold != 0 {
			codes = append(codes, "1")
		}
		if s&Dim != 0 {
			codes = append(codes, "2")
		}
		if s&Underline != 0 {
			codes = append(codes, "4")
		}
		if s&Reverse != 0 {
			codes = append(codes, "7")
		}
	}
	if t.Color {
		if fg := (s & fgMask) >> 8; fg != 0 {
			codes = append(codes, strconv.Itoa(30+int(fg)))
		}
	}
	if len(codes) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

// render writes a line with this theme's escape sequences.
func (t Theme) render(l Line) string {
	var b strings.Builder
	for _, sp := range l {
		if sp.Text == "" {
			continue
		}
		if pre := t.sgr(sp.Style); pre != "" {
			b.WriteString(pre)
			b.WriteString(sp.Text)
			b.WriteString("\x1b[0m")
		} else {
			b.WriteString(sp.Text)
		}
	}
	return b.String()
}

func padTo(s string, have, want int) string {
	if have >= want {
		return s
	}
	return s + strings.Repeat(" ", want-have)
}

var _ = utf8.RuneError
