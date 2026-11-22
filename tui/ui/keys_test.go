package ui

import (
	"reflect"
	"testing"
)

func TestParseKeys(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []Key
	}{
		{"letters", "jk", []Key{{Type: KeyRune, Rune: 'j'}, {Type: KeyRune, Rune: 'k'}}},
		{"enter cr", "\r", []Key{{Type: KeyEnter}}},
		{"enter lf", "\n", []Key{{Type: KeyEnter}}},
		{"tab", "\t", []Key{{Type: KeyTab}}},
		{"backtab", "\x1b[Z", []Key{{Type: KeyBacktab}}},
		{"backspace 7f", "\x7f", []Key{{Type: KeyBackspace}}},
		{"backspace 08", "\x08", []Key{{Type: KeyBackspace}}},
		{"ctrl-c", "\x03", []Key{{Type: KeyCtrlC}}},
		{"ctrl-u", "\x15", []Key{{Type: KeyCtrlU}}},
		{"arrows csi", "\x1b[A\x1b[B\x1b[C\x1b[D", []Key{{Type: KeyUp}, {Type: KeyDown}, {Type: KeyRight}, {Type: KeyLeft}}},
		{"arrows ss3", "\x1bOA\x1bOB", []Key{{Type: KeyUp}, {Type: KeyDown}}},
		{"page", "\x1b[5~\x1b[6~", []Key{{Type: KeyPgUp}, {Type: KeyPgDn}}},
		{"home end", "\x1b[H\x1b[F\x1b[1~\x1b[4~\x1b[7~\x1b[8~", []Key{{Type: KeyHome}, {Type: KeyEnd}, {Type: KeyHome}, {Type: KeyEnd}, {Type: KeyHome}, {Type: KeyEnd}}},
		{"delete", "\x1b[3~", []Key{{Type: KeyDelete}}},
		{"modified arrow", "\x1b[1;5A", []Key{{Type: KeyUp}}},
		{"utf8 rune", "é", []Key{{Type: KeyRune, Rune: 0xe9}}},
		{"unknown csi is swallowed", "\x1b[99~x", []Key{{Type: KeyRune, Rune: 'x'}}},
		{"bracketed paste markers", "\x1b[200~\x1b[201~", []Key{{Type: KeyPasteStart}, {Type: KeyPasteEnd}}},
		{"lone esc when final", "\x1b", []Key{{Type: KeyEsc}}},
		{"alt chord is a plain esc and the letter is dropped", "\x1bq", []Key{{Type: KeyEsc}}},
		{"alt chord with a capital", "\x1bA", []Key{{Type: KeyEsc}}},
		{"esc esc", "\x1b\x1b", []Key{{Type: KeyEsc}, {Type: KeyEsc}}},
		{"invalid utf8 is dropped", "a\xffb", []Key{{Type: KeyRune, Rune: 'a'}, {Type: KeyRune, Rune: 'b'}}},
	}
	for _, c := range cases {
		got, rest := ParseKeys([]byte(c.in), true)
		if len(rest) != 0 {
			t.Errorf("%s: leftover %q", c.name, rest)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestParseKeysKeepsIncompleteSequence(t *testing.T) {
	for _, in := range []string{"\x1b", "\x1b[", "\x1b[1;", "\x1bO", "\xc3"} {
		got, rest := ParseKeys([]byte(in), false)
		if len(got) != 0 || string(rest) != in {
			t.Errorf("%q: keys %+v rest %q", in, got, rest)
		}
	}
	got, rest := ParseKeys([]byte("a\x1b["), false)
	if len(got) != 1 || string(rest) != "\x1b[" {
		t.Errorf("keys %+v rest %q", got, rest)
	}
}

func TestParseKeysSplitAcrossReads(t *testing.T) {
	k1, rest := ParseKeys([]byte("\x1b["), false)
	k2, rest := ParseKeys(append(rest, 'A'), false)
	if len(k1) != 0 || len(k2) != 1 || k2[0].Type != KeyUp || len(rest) != 0 {
		t.Fatalf("%+v %+v %q", k1, k2, rest)
	}
}
