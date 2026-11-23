package safety_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/standardnguyen/human-relay/tui/safety"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func utf16le(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// Thresholds quoted from the dashboard's decoder:
//
//	printable = decoded.replace(/[^\x20-\x7e\x09\x0a\x0d\u00a0-\uffff]/g,'').length
//	accept iff printable / decoded.length > 0.9 && decoded.length > 10
func TestTryDecodeBase64Thresholds(t *testing.T) {
	if got, ok := safety.TryDecodeBase64(b64("echo hello world")); !ok || got != "echo hello world" {
		t.Fatalf("got %q %v", got, ok)
	}
	// decoded length must be > 10: exactly 10 is rejected, 11 accepted.
	if _, ok := safety.TryDecodeBase64(b64("0123456789")); ok {
		t.Error("10 characters must be rejected")
	}
	if _, ok := safety.TryDecodeBase64(b64("0123456789a")); !ok {
		t.Error("11 characters must be accepted")
	}
	// Invalid UTF-8 is rejected outright (fatal decode).
	if _, ok := safety.TryDecodeBase64(base64.StdEncoding.EncodeToString([]byte("abcdefghijk\xff\xfe"))); ok {
		t.Error("invalid UTF-8 must be rejected")
	}
	// Printable ratio must be strictly above 0.9: 1 control in 10 is exactly
	// 0.9 and is rejected; 1 in 20 passes.
	ten := "abcdefghi\x01"
	if _, ok := safety.TryDecodeBase64(b64(ten + "")); ok { // also too short, but check ratio path separately below
		t.Error("short")
	}
	twelveWithOneCtl := "abcdefghijk\x01" // 11/12 = 0.9166 > 0.9
	if _, ok := safety.TryDecodeBase64(b64(twelveWithOneCtl)); !ok {
		t.Error("11 of 12 printable should pass")
	}
	exactlyNinety := "abcdefghi\x01" + strings.Repeat("abcdefghi\x01", 1) // 18/20 = 0.9
	if _, ok := safety.TryDecodeBase64(b64(exactlyNinety)); ok {
		t.Error("exactly 0.9 printable must be rejected")
	}
	// C1 controls U+0080..U+009F are not in the printable class (a0-ffff).
	c1 := strings.Repeat("\u0085", 3) + "abcdefghi" // 9/12 = 0.75
	if _, ok := safety.TryDecodeBase64(b64(c1)); ok {
		t.Error("C1 characters are not printable")
	}
	// U+00A0 and up are.
	if _, ok := safety.TryDecodeBase64(b64(strings.Repeat("\u00e9", 12))); !ok {
		t.Error("accented text is printable")
	}
	// Not base64 at all.
	if _, ok := safety.TryDecodeBase64("!!!not-base64!!!"); ok {
		t.Error("garbage accepted")
	}
	// atob: a length of 1 mod 4 is invalid; padding must match.
	if _, ok := safety.TryDecodeBase64("YWJjZGVmZ2hpamtsbQ=="[:19]); ok {
		t.Error("a length of 1 mod 4 must fail")
	}
}

func TestTryDecodeBase64AcceptsMissingPadding(t *testing.T) {
	full := b64("hello hello hello") // multiple of 3? 17 bytes -> padded
	trimmed := strings.TrimRight(full, "=")
	if got, ok := safety.TryDecodeBase64(trimmed); !ok || got != "hello hello hello" {
		t.Fatalf("got %q %v", got, ok)
	}
}

func TestFindBase64BlobsInline(t *testing.T) {
	payload := "echo this is a decoded payload"
	text := "sh -c echo " + b64(payload) + " | base64 -d"
	blobs := safety.FindBase64Blobs(text)
	if len(blobs) != 1 || blobs[0] != payload {
		t.Fatalf("blobs = %q", blobs)
	}
}

func TestFindBase64BlobsMultilineHeredoc(t *testing.T) {
	payload := strings.Repeat("line of text for a heredoc style blob\n", 3)
	enc := b64(payload)
	var wrapped strings.Builder
	for i := 0; i < len(enc); i += 16 {
		end := i + 16
		if end > len(enc) {
			end = len(enc)
		}
		wrapped.WriteString(enc[i:end] + "\n")
	}
	text := "cat <<EOF | base64 -d\n" + wrapped.String() + "EOF"
	blobs := safety.FindBase64Blobs(text)
	found := false
	for _, b := range blobs {
		if b == payload {
			found = true
		}
	}
	if !found {
		t.Fatalf("the wrapped blob was not reassembled: %q", blobs)
	}
}

func TestFindBase64BlobsThresholds(t *testing.T) {
	// A line must have 16+ characters to start a run, and the run needs 20+.
	short := b64("0123456789a") // 16 chars: starts a run but total < 20 unless padded
	if len(short) >= 20 {
		t.Fatalf("test assumption: %d", len(short))
	}
	if blobs := safety.FindBase64Blobs(short); len(blobs) != 0 {
		t.Errorf("under 20 characters must not decode: %q", blobs)
	}
	// 20+ inline characters inside other text.
	if blobs := safety.FindBase64Blobs("x " + b64("exactly long enough text") + " y"); len(blobs) != 1 {
		t.Errorf("inline blob not found: %q", blobs)
	}
	// Same blob twice decodes once (dedup by exact string).
	enc := b64("a payload that is long enough")
	if blobs := safety.FindBase64Blobs(enc + " " + enc); len(blobs) != 1 {
		t.Errorf("duplicates: %q", blobs)
	}
	// Plain words are not base64 payloads.
	if blobs := safety.FindBase64Blobs("just some ordinary words in a command line here"); len(blobs) != 0 {
		t.Errorf("false positive: %q", blobs)
	}
	// No URL-safe alphabet.
	if blobs := safety.FindBase64Blobs("YWJjZGVmZ2hp-_YWJjZGVmZ2hp"); len(blobs) != 0 {
		t.Errorf("URL-safe alphabet matched: %q", blobs)
	}
	// No nested decoding.
	if blobs := safety.FindBase64Blobs(b64(b64("a payload that is long enough"))); len(blobs) != 1 || blobs[0] != b64("a payload that is long enough") {
		t.Errorf("must decode one level only: %q", blobs)
	}
}

// PowerShell gate and match, quoted:
//
//	gate  /(?:powershell|pwsh)(?:\.exe)?\b/i
//	match /[\s]([-\/])(e[a-z]*)\s+([A-Za-z0-9+\/]{16,}={0,2})/gi
//	and 'encodedcommand'.startsWith(flag.toLowerCase())
func TestPowerShellEncodedCommand(t *testing.T) {
	script := "Write-Host 'hi there'"
	enc := utf16le(script)
	for _, flag := range []string{"-e", "-en", "-enc", "-EncodedCommand", "-ENCODEDCOMMAND", "/e", "/EncodedCommand"} {
		text := "powershell.exe -NoProfile " + flag + " " + enc
		got := safety.PowerShellEncoded(text)
		if len(got) != 1 || !got[0].OK || got[0].Decoded != script || got[0].Base64 != enc {
			t.Errorf("flag %s: %+v", flag, got)
		}
	}
	// Not the encoded-command flag.
	for _, flag := range []string{"-ex", "-eab", "-executionpolicy", "-encodedcommandx"} {
		if got := safety.PowerShellEncoded("pwsh " + flag + " " + enc); len(got) != 0 {
			t.Errorf("flag %s must not match: %+v", flag, got)
		}
	}
	// Needs the powershell gate.
	if got := safety.PowerShellEncoded("cmd.exe -enc " + enc); len(got) != 0 {
		t.Errorf("gate missed: %+v", got)
	}
	// Gate is case-insensitive and accepts pwsh.
	if got := safety.PowerShellEncoded("PWSH -Enc " + enc); len(got) != 1 {
		t.Errorf("pwsh gate: %+v", got)
	}
	// The flag must follow whitespace.
	if got := safety.PowerShellEncoded("powershell x-enc " + enc); len(got) != 0 {
		t.Errorf("flag glued to a word matched: %+v", got)
	}
	// Under 16 base64 characters is not matched.
	if got := safety.PowerShellEncoded("powershell -enc YWJj"); len(got) != 0 {
		t.Errorf("short blob matched: %+v", got)
	}
}

func TestPowerShellUndecodable(t *testing.T) {
	// 16+ base64 characters that decode to mostly non-printable bytes.
	junk := base64.StdEncoding.EncodeToString([]byte{1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 6, 0, 7, 0, 8, 0, 9, 0})
	got := safety.PowerShellEncoded("powershell -enc " + junk)
	if len(got) != 1 || got[0].OK || got[0].Base64 != junk {
		t.Fatalf("got %+v", got)
	}
	// Invalid base64 of a valid-looking length (1 mod 4 once padding is stripped).
	got = safety.PowerShellEncoded("powershell -enc AAAAAAAAAAAAAAAAA")
	if len(got) != 1 || got[0].OK {
		t.Fatalf("got %+v", got)
	}
}

func TestPowerShellMultipleAndInSshWrapper(t *testing.T) {
	a, b := utf16le("Get-Process -Name one"), utf16le("Get-Service -Name two")
	text := "ssh host -- powershell -NoProfile -enc " + a + " ; pwsh -e " + b
	got := safety.PowerShellEncoded(text)
	if len(got) != 2 || got[0].Decoded != "Get-Process -Name one" || got[1].Decoded != "Get-Service -Name two" {
		t.Fatalf("got %+v", got)
	}
}

func TestPowerShellBOMAndOddByte(t *testing.T) {
	// A leading BOM is dropped by the decoder; an odd trailing byte becomes U+FFFD.
	raw := []byte{0xFF, 0xFE, 'h', 0, 'i', 0, ' ', 0, 't', 0, 'h', 0, 'e', 0, 'r', 0, 'e', 0, '!'}
	enc := base64.StdEncoding.EncodeToString(raw)
	got := safety.PowerShellEncoded("powershell -enc " + enc)
	if len(got) != 1 || !got[0].OK || !strings.HasPrefix(got[0].Decoded, "hi there") {
		t.Fatalf("got %+v", got)
	}
	if !strings.HasSuffix(got[0].Decoded, "\ufffd") {
		t.Fatalf("odd trailing byte should decode to U+FFFD: %q", got[0].Decoded)
	}
}
