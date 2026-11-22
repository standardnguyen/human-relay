package ui

import (
	"strings"
	"testing"
)

func TestRuneWidth(t *testing.T) {
	cases := map[rune]int{'a': 1, ' ': 1, 0x4e2d: 2, 0xff21: 2, 0x1F600: 2, 0xe9: 1, 0x0430: 1, 0xac00: 2}
	for r, w := range cases {
		if got := runeWidth(r); got != w {
			t.Errorf("runeWidth(%U) = %d, want %d", r, got, w)
		}
	}
}

func TestLineWidth(t *testing.T) {
	l := Line{{Text: "ab"}, {Text: "中", Style: Bold}}
	if l.Width() != 4 {
		t.Fatalf("width = %d", l.Width())
	}
}

func TestWrapHardBreaksAtWidth(t *testing.T) {
	l := Line{{Text: "abcdefghij"}}
	got := wrap(l, 4)
	want := []string{"abcd", "efgh", "ij"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines", len(got))
	}
	for i, w := range want {
		if got[i].Plain() != w {
			t.Errorf("line %d = %q, want %q", i, got[i].Plain(), w)
		}
	}
}

func TestWrapKeepsStylesAcrossBreak(t *testing.T) {
	l := Line{{Text: "ab", Style: Bold}, {Text: "cdef", Style: Dim}}
	got := wrap(l, 3)
	if len(got) != 2 || got[0].Plain() != "abc" || got[1].Plain() != "def" {
		t.Fatalf("got %+v", got)
	}
	if got[0][0].Style != Bold || got[0][1].Style != Dim || got[1][0].Style != Dim {
		t.Fatalf("styles lost: %+v", got)
	}
}

func TestWrapWideRuneNeverSplits(t *testing.T) {
	got := wrap(Line{{Text: "a中中"}}, 3)
	if len(got) != 2 || got[0].Plain() != "a中" || got[1].Plain() != "中" {
		t.Fatalf("got %+v", got)
	}
}

func TestWrapEmptyLineStaysOneLine(t *testing.T) {
	if got := wrap(Line{}, 10); len(got) != 1 {
		t.Fatalf("got %d lines", len(got))
	}
}

func TestClipAddsMarker(t *testing.T) {
	l := Line{{Text: "abcdefghij"}}
	got := clip(l, 6, false)
	if got.Plain() != "abcde~" && got.Plain() != "abcde…" {
		t.Fatalf("got %q", got.Plain())
	}
	if got.Width() > 6 {
		t.Fatalf("too wide: %d", got.Width())
	}
	if clip(l, 20, false).Plain() != "abcdefghij" {
		t.Fatal("short lines pass through")
	}
	if g := clip(l, 6, true); g.Plain() != "abc..." {
		t.Fatalf("ascii clip = %q", g.Plain())
	}
}

func TestThemeSGR(t *testing.T) {
	off := Theme{}
	if off.render(Line{{Text: "x", Style: Bold | FGRed}}) != "x" {
		t.Fatal("no-colour theme must not emit escapes")
	}
	on := Theme{Color: true}
	got := on.render(Line{{Text: "x", Style: Bold | FGRed}, {Text: "y"}})
	if got != "\x1b[1;31mx\x1b[0my" {
		t.Fatalf("got %q", got)
	}
	// Attributes without colour (NO_COLOR) are allowed; colours are not.
	attrs := Theme{Attrs: true}
	if got := attrs.render(Line{{Text: "x", Style: Reverse | FGRed}}); got != "\x1b[7mx\x1b[0m" {
		t.Fatalf("got %q", got)
	}
}

func TestRuneWidthIsConservativeForSymbolBlocks(t *testing.T) {
	for _, r := range []rune{0x1F680, 0x1FA70, 0x1F9FF, 0x2705, 0x2B50, 0x231A} {
		if runeWidth(r) != 2 {
			t.Errorf("runeWidth(%U) = %d, want 2", r, runeWidth(r))
		}
	}
}

// Characters the terminal draws two cells wide are counted as two, so rows are
// cut and wrapped where the terminal would wrap them.
func TestRuneWidthCoversEveryEastAsianWideRange(t *testing.T) {
	wide := [][2]rune{
		{0x25fd, 0x25fe}, {0xa960, 0xa97c}, {0xfe10, 0xfe19}, {0x16fe0, 0x16fe4}, {0x16ff0, 0x16ff1},
		{0x17000, 0x187f7}, {0x18800, 0x18cd5}, {0x18d00, 0x18d08}, {0x1aff0, 0x1affe}, {0x1b000, 0x1b122},
		{0x1b150, 0x1b152}, {0x1b164, 0x1b167}, {0x1b170, 0x1b2fb},
		// already covered ranges stay covered
		{0x1100, 0x115f}, {0x2e80, 0x2e99}, {0x3041, 0x3096}, {0x4e00, 0x9fff}, {0xac00, 0xd7a3}, {0xff01, 0xff60}, {0x1f300, 0x1f64f}, {0x20000, 0x2a6df},
	}
	for _, rg := range wide {
		for _, r := range []rune{rg[0], rg[1], (rg[0] + rg[1]) / 2} {
			if w := runeWidth(r); w != 2 {
				t.Errorf("U+%04X has width %d, want 2", r, w)
			}
		}
	}
	for _, r := range []rune{'a', 'é', 'я', '€', 0xff61, 0xffe8} {
		if runeWidth(r) != 1 {
			t.Errorf("U+%04X should be one cell", r)
		}
	}
}

func TestWrapAndClipNeverExceedTheWidthForTangutText(t *testing.T) {
	l := Line{{Text: "echo " + strings.Repeat("\U00017000", 100)}}
	for _, row := range wrap(l, 60) {
		if row.Width() > 60 {
			t.Fatalf("wrapped row is %d cells", row.Width())
		}
	}
	if c := clip(l, 60, false); c.Width() > 60 {
		t.Fatalf("clipped row is %d cells", c.Width())
	}
}
