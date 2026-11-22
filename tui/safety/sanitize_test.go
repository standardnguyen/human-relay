package safety_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/standardnguyen/human-relay/tui/safety"
)

func plain(s string, ascii bool) string {
	var b strings.Builder
	for _, sg := range safety.Sanitize(s, safety.Options{ASCII: ascii}) {
		b.WriteString(sg.Text)
	}
	return b.String()
}

// noRawControls asserts the invariant every display path relies on: after
// sanitising, nothing the terminal could interpret survives.
func noRawControls(t *testing.T, label, out string) {
	t.Helper()
	for i := 0; i < len(out); {
		r, n := utf8.DecodeRuneInString(out[i:])
		if r == utf8.RuneError && n == 1 {
			t.Fatalf("%s: invalid UTF-8 byte %#x survived in %q", label, out[i], out)
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("%s: control rune %U survived in %q", label, r, out)
		}
		switch r {
		case 0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0x2060, 0xFEFF, 0x061C,
			0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069,
			0x2028, 0x2029, 0x0085:
			t.Fatalf("%s: invisible/bidi rune %U survived in %q", label, r, out)
		}
		i += n
	}
}

func TestHostileInputsLeaveNoRawControlBytes(t *testing.T) {
	hostile := map[string]string{
		"CSI clear screen":     "before\x1b[2Jafter",
		"OSC 52 clipboard":     "x\x1b]52;c;aGVsbG8=\x07y",
		"OSC 52 ST":            "x\x1b]52;c;aGVsbG8=\x1b\\y",
		"OSC 8 hyperlink":      "\x1b]8;;http://evil.example/\x1b\\click\x1b]8;;\x1b\\",
		"OSC 0 title":          "\x1b]0;pwned\x07",
		"DCS":                  "\x1bP1$r\x1b\\",
		"C1 CSI as UTF-8":      "a\u009b2Jb",
		"C1 CSI as a raw byte": "a\x9b2Jb",
		"C1 OSC raw":           "a\x9d0;t\x9cb",
		"bidi override":        "safe\u202egnp.exe",
		"bidi isolate":         "a\u2066b\u2069c",
		"zero width":           "pass\u200bword\u200c\u200d\u2060\ufeff",
		"line separator":       "a\u2028b\u2029c",
		"NEL":                  "a\u0085b",
		"carriage return":      "real\rfake",
		"backspace overwrite":  "abc\b\b\bxyz",
		"NUL":                  "a\x00b",
		"bell":                 "a\x07b",
		"DEL":                  "a\x7fb",
		"form feed, VT":        "a\fb\vc",
		"homoglyph cyrillic":   "p\u0430ssword",
		"combining":            "e\u0301\u0301\u0301",
		"invalid utf8":         "a\xff\xfeb\xc3",
		"tab and newline":      "a\tb\nc",
		"private use":          "a\ue000b",
		"noncharacter":         "a\uffffb",
		"language tag":         "a\U000E0041b",
	}
	for name, in := range hostile {
		for _, ascii := range []bool{false, true} {
			out := plain(in, ascii)
			noRawControls(t, name, out)
			if ascii {
				for i := 0; i < len(out); i++ {
					if out[i] >= 0x80 {
						t.Fatalf("%s: ascii mode emitted byte %#x in %q", name, out[i], out)
					}
				}
			}
		}
	}
}

func TestEscapeRenderedVisibly(t *testing.T) {
	cases := map[string]string{
		"a\x1b[2Jb":      `a\x1b[2Jb`,
		"a\x00b":         `a\x00b`,
		"a\x7fb":         `a\x7fb`,
		"a\x07b":         `a\x07b`,
		"a\tb":           `a\tb`,
		"a\nb":           `a\nb`,
		"a\rb":           `a\rb`,
		"a\x9bb":         `a\x9bb`,
		"a\xffb":         `a\xffb`,
		"a\u009bb":       `a<U+009B>b`,
		"a\u202eb":       `a<U+202E>b`,
		"a\u200bb":       `a<U+200B>b`,
		"a\ufeffb":       `a<U+FEFF>b`,
		"a\u2028b":       `a<U+2028>b`,
		"a\u00a0b":       `a<U+00A0>b`,
		"e\u0301":        `e<U+0301>`,
		"a\ue000b":       `a<U+E000>b`,
		"a\U000E0041b":   `a<U+E0041>b`,
		"plain text 123": "plain text 123",
	}
	for in, want := range cases {
		if got := plain(in, false); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOSC52PayloadIsVisibleNotExecutable(t *testing.T) {
	out := plain("x\x1b]52;c;aGVsbG8=\x07y", false)
	if !strings.Contains(out, `\x1b]52;c;aGVsbG8=\x07`) {
		t.Fatalf("the sequence should be readable as text, got %q", out)
	}
}

func TestNonASCIIIsShownAndFlagged(t *testing.T) {
	segs := safety.Sanitize("p\u0430ss", safety.Options{})
	var flagged []safety.Seg
	for _, s := range segs {
		if s.Flag == safety.FlagNonASCII {
			flagged = append(flagged, s)
		}
	}
	if len(flagged) != 1 || flagged[0].Text != "\u0430" || flagged[0].Rune != 0x0430 {
		t.Fatalf("segments = %+v", segs)
	}
	// In ASCII mode the same rune is shown as its codepoint.
	segs = safety.Sanitize("p\u0430ss", safety.Options{ASCII: true})
	joined := ""
	for _, s := range segs {
		joined += s.Text
	}
	if joined != "p<U+0430>ss" {
		t.Fatalf("ascii rendering = %q", joined)
	}
}

func TestControlSegmentsAreFlaggedControl(t *testing.T) {
	segs := safety.Sanitize("a\x1bb", safety.Options{})
	if len(segs) != 3 || segs[1].Flag != safety.FlagControl || segs[1].Text != `\x1b` {
		t.Fatalf("segments = %+v", segs)
	}
}

func TestAllowedTypographyIsNotFlagged(t *testing.T) {
	// Same allowance as the dashboard's non-ASCII check: en/em dash, curly
	// quotes and the ellipsis are ordinary prose.
	in := "a \u2013 b \u2014 c \u2018d\u2019 \u201ce\u201d\u2026"
	for _, s := range safety.Sanitize(in, safety.Options{}) {
		if s.Flag != safety.FlagNone {
			t.Fatalf("%q flagged: %+v", s.Text, s)
		}
	}
	if got := plain(in, false); got != in {
		t.Fatalf("got %q", got)
	}
	// ASCII mode maps them to ASCII look-alikes rather than codepoint tags.
	if got := plain(in, true); got != "a - b - c 'd' \"e\"..." {
		t.Fatalf("ascii = %q", got)
	}
}

func TestLinesSplitOnNewlineAndCRLF(t *testing.T) {
	lines := safety.Lines("one\r\ntwo\nthree\rfour\n", safety.Options{})
	var got []string
	for _, l := range lines {
		s := ""
		for _, sg := range l {
			s += sg.Text
		}
		got = append(got, s)
	}
	want := []string{"one", "two", `three\rfour`, ""}
	if len(got) != len(want) {
		t.Fatalf("lines = %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLinesNeverContainRawControls(t *testing.T) {
	for _, l := range safety.Lines("a\x1b[31mred\x1b[0m\n\x1b]0;t\x07b\n\u202e\n", safety.Options{}) {
		s := ""
		for _, sg := range l {
			s += sg.Text
		}
		noRawControls(t, "line", s)
	}
}

func TestPlainHelperMatchesSanitize(t *testing.T) {
	in := "a\x1b[2Jb\u0430"
	if safety.Plain(in, safety.Options{}) != plain(in, false) {
		t.Fatal("Plain must equal the joined segments")
	}
}

func TestInlineOneLine(t *testing.T) {
	got := safety.Plain("line1\nline2\tx", safety.Options{})
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("got %q", got)
	}
}

func TestDescribeScript(t *testing.T) {
	if got := safety.ScriptOf(0x0430); got != "Cyrillic" {
		t.Fatalf("ScriptOf(U+0430) = %q", got)
	}
	if got := safety.ScriptOf(0x03BF); got != "Greek" {
		t.Fatalf("ScriptOf(U+03BF) = %q", got)
	}
	if got := safety.ScriptOf('a'); got != "Latin" {
		t.Fatalf("ScriptOf(a) = %q", got)
	}
	if got := safety.ScriptOf(0x202E); got != "Common" && got != "" {
		t.Fatalf("ScriptOf(RLO) = %q", got)
	}
}

// A long single line is sanitized in linear time: appending to a growing
// string per rune would copy the whole line again for every character.
func TestSanitizeLongPlainLineAllocatesLinearly(t *testing.T) {
	line := strings.Repeat("x", 20000)
	allocs := testing.AllocsPerRun(3, func() { safety.Sanitize(line, safety.Options{}) })
	if allocs > 100 {
		t.Fatalf("%.0f allocations for a 20000 character line, want a handful", allocs)
	}
	segs := safety.Sanitize(line, safety.Options{})
	if len(segs) != 1 || segs[0].Text != line {
		t.Fatalf("a plain line is one segment, got %d", len(segs))
	}
}

func TestSanitizeLongMixedLineKeepsSegments(t *testing.T) {
	in := strings.Repeat("ab\x1bcd", 5000)
	if got := safety.Plain(in, safety.Options{}); got != strings.Repeat(`ab\x1bcd`, 5000) {
		t.Fatal("mixed line changed")
	}
}
