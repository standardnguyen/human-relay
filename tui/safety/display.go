package safety

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/standardnguyen/human-relay/tui/api"
)

// Banner texts shown on pending requests.
const (
	ShellBanner    = "Shell execution \u2014 command will be interpreted by sh -c. Review carefully."
	NonASCIIBanner = "Non-ASCII characters detected \u2014 check for homoglyph substitution."
)

// Display limits.
const (
	ReasonLimit   = 200
	HTTPBodyLimit = 1024
	LinesBadgeMin = 5 // a command of more than this many lines gets the lines badge
)

// FormatSize renders a byte count as 12B, 1.5KB or 2.0MB. Tenths round half
// up, like JavaScript's toFixed(1).
func FormatSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1048576:
		t := (n*10 + 512) / 1024
		return fmt.Sprintf("%d.%dKB", t/10, t%10)
	default:
		t := (n*10 + 524288) / 1048576
		return fmt.Sprintf("%d.%dMB", t/10, t%10)
	}
}

// CountLines counts lines of text; a trailing newline ends the last line
// instead of opening another one, and empty text has none.
func CountLines(text string) int {
	if text == "" {
		return 0
	}
	n := strings.Count(text, "\n") + 1
	if strings.HasSuffix(text, "\n") {
		n--
	}
	return n
}

// CommandLineCount is the number of lines in "command arg arg ...", the figure
// behind the "N lines" badge.
func CommandLineCount(r api.Request) int {
	t := r.Command
	if len(r.Args) > 0 {
		t += " " + strings.Join(r.Args, " ")
	}
	return strings.Count(t, "\n") + 1
}

// CommandText is the command and its arguments joined by single spaces.
func CommandText(r api.Request) string {
	if len(r.Args) == 0 {
		return r.Command
	}
	return r.Command + " " + strings.Join(r.Args, " ")
}

var secretHeader = regexp.MustCompile(`(?i)^(authorization|x-api-key|cookie)`)

// RedactHeader returns the value to display for a request header: credential
// headers (by name prefix) show their first 12 characters and "...".
func RedactHeader(key, value string) string {
	if !secretHeader.MatchString(key) {
		return value
	}
	cut, _ := TruncateUTF16(value, 12)
	return cut + "..."
}

// TruncateUTF16 cuts s to at most max UTF-16 code units (the unit JavaScript
// counts in), never splitting a character, and reports whether it cut.
func TruncateUTF16(s string, max int) (string, bool) {
	n := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > max {
			return s[:i], true
		}
		n += w
	}
	return s, false
}

// ReasonPreview shortens a reason to 200 characters plus "..." and says whether it did.
func ReasonPreview(reason string) (string, bool) {
	cut, did := TruncateUTF16(reason, ReasonLimit)
	if did {
		return cut + "...", true
	}
	return reason, false
}

func prettyJSON(s string) (string, bool) {
	if !json.Valid([]byte(s)) {
		return s, false
	}
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(s), "", "  "); err != nil {
		return s, false
	}
	return b.String(), true
}

// PrettyJSONOrRaw indents s when it is valid JSON and returns it unchanged otherwise.
func PrettyJSONOrRaw(s string) string {
	p, _ := prettyJSON(s)
	return p
}

// HTTPBodyPreview is the body as shown on the approval screen: pretty-printed
// when it is JSON, and cut to 1024 characters with the truncated flag set.
func HTTPBodyPreview(body string) (string, bool) {
	preview, truncated := TruncateUTF16(body, HTTPBodyLimit)
	if p, ok := prettyJSON(body); ok {
		preview = p
		if cut, did := TruncateUTF16(p, HTTPBodyLimit); did {
			preview, truncated = cut, true
		}
	}
	return preview, truncated
}

// HTTPBodyFull is the whole body, pretty-printed when it is JSON.
func HTTPBodyFull(body string) string { return PrettyJSONOrRaw(body) }

// IsValidUTF8 reports whether b is valid UTF-8.
func IsValidUTF8(b []byte) bool { return utf8.Valid(b) }

// TruncateUTF16Cut reports whether s is longer than max UTF-16 code units.
func TruncateUTF16Cut(s string, max int) bool {
	_, cut := TruncateUTF16(s, max)
	return cut
}
