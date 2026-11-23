package ui

import (
	"strings"
	"testing"
)

func TestFrameWriterOnlyRewritesChangedLines(t *testing.T) {
	var fw frameWriter
	first := fw.diff("one\ntwo\nthree", 3)
	for _, want := range []string{"\x1b[1;1H", "one", "\x1b[2;1H", "two", "\x1b[3;1H", "three"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first frame missing %q: %q", want, first)
		}
	}
	if got := fw.diff("one\ntwo\nthree", 3); got != "" {
		t.Fatalf("an unchanged frame must write nothing, got %q", got)
	}
	got := fw.diff("one\nTWO\nthree", 3)
	if !strings.Contains(got, "\x1b[2;1H") || !strings.Contains(got, "TWO") || strings.Contains(got, "one") || strings.Contains(got, "three") {
		t.Fatalf("only line 2 should be rewritten: %q", got)
	}
	fw.invalidate()
	if got := fw.diff("one\nTWO\nthree", 3); !strings.Contains(got, "one") {
		t.Fatal("after invalidate everything is redrawn")
	}
}

func TestFrameWriterClearsRestOfLine(t *testing.T) {
	var fw frameWriter
	got := fw.diff("a", 1)
	// The line is cleared before the text goes out, so a terminal that parks
	// the cursor at the right margin cannot erase the last character.
	if !strings.Contains(got, "\x1b[1;1H\x1b[0m\x1b[Ka\x1b[0m") {
		t.Fatalf("lines must be cleared first, then written, then reset: %q", got)
	}
}
