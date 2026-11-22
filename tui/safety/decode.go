package safety

import (
	"encoding/base64"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// utf16Len is the length JavaScript reports for s: UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// printableUnits counts the UTF-16 units of s inside the class
// [\x20-\x7e\x09\x0a\x0d -￿], and returns it with the total.
func printableUnits(s string) (printable, total int) {
	for _, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		total += w
		if (r >= 0x20 && r <= 0x7e) || r == 0x09 || r == 0x0a || r == 0x0d || r >= 0xa0 {
			printable += w
		}
	}
	return printable, total
}

// atob decodes the way a browser's atob does: with trailing = optional, and
// failing on a length of 1 mod 4 or any character outside the standard alphabet.
func atob(s string) ([]byte, bool) {
	if len(s)%4 == 0 {
		s = strings.TrimSuffix(s, "=")
		s = strings.TrimSuffix(s, "=")
	}
	if len(s)%4 == 1 {
		return nil, false
	}
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}

// TryDecodeBase64 decodes a base64 blob as UTF-8 and accepts the result when
// more than 90% of it is printable and it is longer than 10 characters:
//
//	printable / decoded.length > 0.9 && decoded.length > 10
func TryDecodeBase64(b64 string) (string, bool) {
	b, ok := atob(b64)
	if !ok {
		return "", false
	}
	b = []byte(strings.TrimPrefix(string(b), "\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		return "", false
	}
	s := string(b)
	p, n := printableUnits(s)
	if n > 10 && p*10 > n*9 {
		return s, true
	}
	return "", false
}

// jsTrim trims what JavaScript's String.prototype.trim trims.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
}

var (
	b64LineStart = regexp.MustCompile(`^[A-Za-z0-9+/]{16,}={0,2}$`)
	b64LineCont  = regexp.MustCompile(`^[A-Za-z0-9+/]{4,}={0,2}$`)
	b64Inline    = regexp.MustCompile(`[A-Za-z0-9+/]{20,}={0,2}`)
)

// FindBase64Blobs returns the decoded text of every base64 blob in text:
// first runs of base64-only lines (a line of 16+ characters starts a run, each
// further line needs 4+, the run needs 20+ in total), then inline strings of
// 20+ characters. Each exact string decodes once; there is no nested decoding.
func FindBase64Blobs(text string) []string {
	var blobs []string
	seen := map[string]bool{}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); {
		if !b64LineStart.MatchString(jsTrim(lines[i])) {
			i++
			continue
		}
		var run strings.Builder
		for i < len(lines) && b64LineCont.MatchString(jsTrim(lines[i])) {
			run.WriteString(jsTrim(lines[i]))
			i++
		}
		b := run.String()
		if len(b) >= 20 && !seen[b] {
			if d, ok := TryDecodeBase64(b); ok {
				blobs = append(blobs, d)
				seen[b] = true
			}
		}
	}
	for _, m := range b64Inline.FindAllString(text, -1) {
		if seen[m] {
			continue
		}
		if d, ok := TryDecodeBase64(m); ok {
			blobs = append(blobs, d)
			seen[m] = true
		}
	}
	return blobs
}

// jsSpace is JavaScript's \s, which is wider than RE2's.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	psGate  = regexp.MustCompile(`(?i)(?:powershell|pwsh)(?:\.exe)?\b`)
	psMatch = regexp.MustCompile(`(?i)[` + jsSpace + `]([-/])(e[a-z]*)[` + jsSpace + `]+([A-Za-z0-9+/]{16,}={0,2})`)
)

// PSResult is one PowerShell -EncodedCommand argument found in a command.
type PSResult struct {
	Base64  string
	Decoded string
	OK      bool
}

func tryDecodeUTF16LE(b64 string) (string, bool) {
	b, ok := atob(b64)
	if !ok {
		return "", false
	}
	units := make([]uint16, 0, len(b)/2+1)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if len(b)%2 == 1 {
		units = append(units, 0xFFFD)
	}
	if len(units) > 0 && units[0] == 0xFEFF {
		units = units[1:]
	}
	s := string(utf16.Decode(units))
	p, n := printableUnits(s)
	if n > 0 && p*10 > n*9 {
		return s, true
	}
	return "", false
}

// PowerShellEncoded finds powershell/pwsh -EncodedCommand arguments (the flag
// may be any prefix of "encodedcommand", started with - or /) and decodes the
// base64 UTF-16LE script they carry. A blob that does not decode is returned
// with OK false so it can be shown as undecodable instead of hidden.
func PowerShellEncoded(text string) []PSResult {
	if !psGate.MatchString(text) {
		return nil
	}
	var out []PSResult
	for _, m := range psMatch.FindAllStringSubmatch(text, -1) {
		if !strings.HasPrefix("encodedcommand", strings.ToLower(m[2])) {
			continue
		}
		d, ok := tryDecodeUTF16LE(m[3])
		out = append(out, PSResult{Base64: m[3], Decoded: d, OK: ok})
	}
	return out
}
