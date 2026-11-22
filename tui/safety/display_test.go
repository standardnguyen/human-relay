package safety_test

import (
	"strings"
	"testing"

	"github.com/standardnguyen/human-relay/tui/api"
	"github.com/standardnguyen/human-relay/tui/safety"
)

func TestFormatSize(t *testing.T) {
	cases := map[int]string{
		0: "0B", 1: "1B", 1023: "1023B",
		1024: "1.0KB", 1280: "1.3KB", // 1.25 rounds up, as toFixed does
		1536: "1.5KB", 1048575: "1024.0KB",
		1048576: "1.0MB", 1048576 + 524288: "1.5MB", 5 * 1048576: "5.0MB",
	}
	for n, want := range cases {
		if got := safety.FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestCountLines(t *testing.T) {
	cases := map[string]int{"": 0, "a": 1, "a\n": 1, "a\nb": 2, "a\nb\n": 2, "\n": 1, "\n\n": 2}
	for in, want := range cases {
		if got := safety.CountLines(in); got != want {
			t.Errorf("CountLines(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCommandLineCount(t *testing.T) {
	r := api.Request{Command: "echo", Args: []string{"a\nb\nc", "d\ne"}}
	if got := safety.CommandLineCount(r); got != 4 {
		t.Fatalf("got %d", got)
	}
	if got := safety.CommandLineCount(api.Request{Command: "ls"}); got != 1 {
		t.Fatalf("got %d", got)
	}
}

func TestRedactHeader(t *testing.T) {
	cases := []struct{ k, v, want string }{
		{"Authorization", "Bearer abcdefghijklmnop", "Bearer abcde..."},
		{"authorization", "short", "short..."},
		{"X-API-Key", "0123456789abcdefXYZ", "0123456789ab..."},
		{"Cookie", "a=b", "a=b..."},
		{"cookie-extra", "0123456789abcdef", "0123456789ab..."}, // prefix match
		{"Content-Type", "application/json", "application/json"},
		{"Proxy-Authorization", "Basic abcdefghijklmnop", "Basic abcdefghijklmnop"}, // not in the prefix set: shown whole
	}
	for _, c := range cases {
		if got := safety.RedactHeader(c.k, c.v); got != c.want {
			t.Errorf("RedactHeader(%q,%q) = %q, want %q", c.k, c.v, got, c.want)
		}
	}
}

func TestTruncateUTF16(t *testing.T) {
	s, cut := safety.TruncateUTF16("hello", 10)
	if s != "hello" || cut {
		t.Fatalf("%q %v", s, cut)
	}
	s, cut = safety.TruncateUTF16("hello", 5)
	if s != "hello" || cut {
		t.Fatalf("%q %v", s, cut)
	}
	s, cut = safety.TruncateUTF16("hello world", 5)
	if s != "hello" || !cut {
		t.Fatalf("%q %v", s, cut)
	}
	// An astral rune counts as two units and is never split.
	s, cut = safety.TruncateUTF16("ab\U0001F600cd", 3)
	if s != "ab" || !cut {
		t.Fatalf("%q %v", s, cut)
	}
	s, _ = safety.TruncateUTF16("ab\U0001F600cd", 4)
	if s != "ab\U0001F600" {
		t.Fatalf("%q", s)
	}
}

func TestReasonPreview(t *testing.T) {
	short := strings.Repeat("a", 200)
	if s, cut := safety.ReasonPreview(short); s != short || cut {
		t.Fatal("200 characters is not truncated")
	}
	long := strings.Repeat("a", 201)
	s, cut := safety.ReasonPreview(long)
	if !cut || s != strings.Repeat("a", 200)+"..." {
		t.Fatalf("%q %v", s, cut)
	}
}

func TestHTTPBodyPreview(t *testing.T) {
	// Not JSON: raw text, cut at 1024.
	text, cut := safety.HTTPBodyPreview("plain body")
	if text != "plain body" || cut {
		t.Fatalf("%q %v", text, cut)
	}
	text, cut = safety.HTTPBodyPreview(strings.Repeat("x", 2000))
	if len(text) != 1024 || !cut {
		t.Fatalf("len %d cut %v", len(text), cut)
	}
	// JSON: pretty-printed with two spaces.
	text, cut = safety.HTTPBodyPreview(`{"a":1,"b":[1,2]}`)
	want := "{\n  \"a\": 1,\n  \"b\": [\n    1,\n    2\n  ]\n}"
	if text != want || cut {
		t.Fatalf("%q %v", text, cut)
	}
	// Pretty output is re-truncated at 1024.
	big := `{"k":"` + strings.Repeat("v", 3000) + `"}`
	text, cut = safety.HTTPBodyPreview(big)
	if len(text) != 1024 || !cut {
		t.Fatalf("len %d cut %v", len(text), cut)
	}
	// A JSON body whose raw form fits but whose pretty form does not.
	arr := "[" + strings.TrimSuffix(strings.Repeat("1,", 400), ",") + "]"
	if len(arr) > 1024 {
		t.Fatalf("test setup: %d", len(arr))
	}
	text, cut = safety.HTTPBodyPreview(arr)
	if !cut || len(text) != 1024 {
		t.Fatalf("len %d cut %v", len(text), cut)
	}
}

func TestHTTPBodyFull(t *testing.T) {
	got := safety.HTTPBodyFull(`{"a":1}`)
	if got != "{\n  \"a\": 1\n}" {
		t.Fatalf("%q", got)
	}
	if got := safety.HTTPBodyFull("raw"); got != "raw" {
		t.Fatalf("%q", got)
	}
}

func TestPrettyJSONOrRaw(t *testing.T) {
	if got := safety.PrettyJSONOrRaw(`{"a":[]}`); got != "{\n  \"a\": []\n}" {
		t.Fatalf("%q", got)
	}
	if got := safety.PrettyJSONOrRaw("not json"); got != "not json" {
		t.Fatalf("%q", got)
	}
}

func TestBannerText(t *testing.T) {
	if safety.ShellBanner != "Shell execution \u2014 command will be interpreted by sh -c. Review carefully." {
		t.Fatalf("%q", safety.ShellBanner)
	}
	if safety.NonASCIIBanner != "Non-ASCII characters detected \u2014 check for homoglyph substitution." {
		t.Fatalf("%q", safety.NonASCIIBanner)
	}
}
