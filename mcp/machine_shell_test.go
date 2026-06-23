package mcp

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/standardnguyen/human-relay/machines"
)

func TestPwshQuote(t *testing.T) {
	cases := map[string]string{
		`C:\Users\esthie\f.txt`: `'C:\Users\esthie\f.txt'`,
		`it's`:                  `'it''s'`,
		`plain`:                 `'plain'`,
	}
	for in, want := range cases {
		if got := pwshQuote(in); got != want {
			t.Errorf("pwshQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// decodeEncodedCommand reverses psEncodedCommand: base64 -> UTF-16LE -> string.
func decodeEncodedCommand(t *testing.T, b64 string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE byte length not even: %d", len(raw))
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[i*2]) | uint16(raw[i*2+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestPSEncodedCommandRoundTrip(t *testing.T) {
	script := "$path='C:\\x'; [IO.File]::WriteAllBytes($path,$bytes)"
	if got := decodeEncodedCommand(t, psEncodedCommand(script)); got != script {
		t.Fatalf("round trip = %q, want %q", got, script)
	}
}

func TestMachineExecRemotePosix(t *testing.T) {
	m := &machines.Machine{Name: "box", Shell: machines.ShellPosix}

	// shell=false: separate argv
	got := machineExecRemote(m, "docker", []string{"ps", "-a"}, false)
	if len(got) != 3 || got[0] != "docker" || got[2] != "-a" {
		t.Fatalf("posix non-shell: %v", got)
	}
	// shell=true: one joined string
	got = machineExecRemote(m, "ls", []string{"-l", "/tmp"}, true)
	if len(got) != 1 || got[0] != "ls -l /tmp" {
		t.Fatalf("posix shell: %v", got)
	}
}

func TestMachineExecRemotePowerShell(t *testing.T) {
	m := &machines.Machine{Name: "win", Shell: machines.ShellPowerShell}

	// shell=false: pass binary + args straight through
	got := machineExecRemote(m, "docker", []string{"ps"}, false)
	if len(got) != 2 || got[0] != "docker" || got[1] != "ps" {
		t.Fatalf("powershell non-shell: %v", got)
	}

	// shell=true: powershell -NoProfile -EncodedCommand <b64 of joined cmd>
	got = machineExecRemote(m, "Get-Process", []string{"|", "Select", "Name"}, true)
	if len(got) != 4 || got[0] != "powershell" || got[1] != "-NoProfile" || got[2] != "-EncodedCommand" {
		t.Fatalf("powershell shell invoke shape: %v", got)
	}
	if decoded := decodeEncodedCommand(t, got[3]); decoded != "Get-Process | Select Name" {
		t.Fatalf("powershell encoded command = %q", decoded)
	}
}

func TestMachineWriteRemotePosix(t *testing.T) {
	m := &machines.Machine{Name: "box", Shell: machines.ShellPosix}
	remote, stdin := machineWriteRemote(m, "/etc/foo.conf", "0644", []byte("hello"))
	if len(remote) != 1 || !strings.Contains(remote[0], "cat > '/etc/foo.conf'") || !strings.Contains(remote[0], "chmod 0644") {
		t.Fatalf("posix write remote: %v", remote)
	}
	if string(stdin) != "hello" {
		t.Fatalf("posix stdin should be raw content, got %q", stdin)
	}
}

func TestMachineWriteRemotePowerShell(t *testing.T) {
	m := &machines.Machine{Name: "win", Shell: machines.ShellPowerShell}
	content := []byte("hello\x00binary")
	remote, stdin := machineWriteRemote(m, `C:\Users\esthie\f.dat`, "0644", content)

	if len(remote) != 4 || remote[0] != "powershell" || remote[2] != "-EncodedCommand" {
		t.Fatalf("powershell write remote shape: %v", remote)
	}
	// stdin must be base64 of the content (binary-safe), not the raw bytes.
	if string(stdin) != base64.StdEncoding.EncodeToString(content) {
		t.Fatalf("powershell stdin should be base64 of content, got %q", stdin)
	}
	// the decoded script must embed the path as a single-quoted literal and
	// decode stdin base64 to bytes.
	script := decodeEncodedCommand(t, remote[3])
	if !strings.Contains(script, `$path='C:\Users\esthie\f.dat'`) {
		t.Fatalf("script missing quoted path: %s", script)
	}
	if !strings.Contains(script, "FromBase64String") || !strings.Contains(script, "WriteAllBytes") {
		t.Fatalf("script missing decode/write: %s", script)
	}
}

// TestPSWriteFileScriptKeepsBase64 guards the inline path: the script that
// receives base64 on stdin must still call FromBase64String, so the bytes
// land on disk verbatim. This is the counterweight to the new raw-bytes
// script (psWriteFileRawScript) — the two scripts have different shapes by
// design, never alias one to the other.
//
// Also guards the stdin-read approach: we must use the streaming
// [Console]::OpenStandardInput() + Stream.Read loop, NOT
// [Console]::In.ReadToEnd(). ReadToEnd() blocks indefinitely on Windows
// PowerShell when stdin is a pipe and the content is >10 KiB (the SSH
// transport never signals EOF while the writer is still going), so any
// >10 KiB write would hang. The streaming loop returns 0 on EOF and exits
// cleanly.
func TestPSWriteFileScriptKeepsBase64(t *testing.T) {
	script := psWriteFileScript(`C:\Users\esthie\note.txt`)
	if !strings.Contains(script, "FromBase64String") {
		t.Fatalf("psWriteFileScript should decode stdin as base64 (inline path sends b64): %s", script)
	}
	if !strings.Contains(script, "WriteAllBytes") {
		t.Fatalf("psWriteFileScript should write bytes: %s", script)
	}
	if !strings.Contains(script, "OpenStandardInput") {
		t.Fatalf("psWriteFileScript must stream stdin via OpenStandardInput (ReadToEnd hangs on >10 KiB piped stdin): %s", script)
	}
	if !strings.Contains(script, "$s.Read(") {
		t.Fatalf("psWriteFileScript must read via a Stream.Read loop into a buffer (ReadToEnd hangs on >10 KiB piped stdin): %s", script)
	}
	if strings.Contains(script, "ReadToEnd()") {
		t.Fatalf("psWriteFileScript must NOT use [Console]::In.ReadToEnd() (hangs on >10 KiB piped stdin via SSH): %s", script)
	}
	if !strings.Contains(script, `'C:\Users\esthie\note.txt'`) {
		t.Fatalf("psWriteFileScript should embed the path as a single-quoted literal: %s", script)
	}
}

// TestPSWriteFileRawScript is the source-stream counterpart: when raw bytes
// arrive on stdin (piped from a remote `cat` over SSH), the script must NOT
// try to base64-decode them — it must write the bytes verbatim. Sharing one
// script between the inline (b64 stdin) and source-stream (raw stdin) paths
// would corrupt every source-stream file by trying to base64-decode binary
// content. The Trim semantics from psKeyAppendScript don't carry over: file
// bytes can legitimately end in whitespace or newlines, so we must NOT trim.
//
// Also guards the stdin-read approach: we must use the streaming
// [Console]::OpenStandardInput() + Stream.Read loop, NOT
// [Console]::In.ReadToEnd(). ReadToEnd() would have hung on >10 KiB piped
// stdin (the SSH transport) AND would have returned a String that we'd then
// have passed to WriteAllBytes (which wants byte[]) — a type error that
// would have either crashed or corrupted binary content. The MemoryStream
// approach accumulates byte[] and WriteAllBytes($path, $ms.ToArray()) gets
// a byte[] directly.
func TestPSWriteFileRawScript(t *testing.T) {
	script := psWriteFileRawScript(`C:\Users\esthie\data.bin`)
	if strings.Contains(script, "FromBase64String") {
		t.Fatalf("psWriteFileRawScript must NOT decode base64 (source-stream pipes raw bytes from `cat`): %s", script)
	}
	if !strings.Contains(script, "WriteAllBytes") {
		t.Fatalf("psWriteFileRawScript should write bytes: %s", script)
	}
	if !strings.Contains(script, "OpenStandardInput") {
		t.Fatalf("psWriteFileRawScript must stream stdin via OpenStandardInput (ReadToEnd hangs on >10 KiB piped stdin): %s", script)
	}
	if !strings.Contains(script, "$s.Read(") {
		t.Fatalf("psWriteFileRawScript must read via a Stream.Read loop into a buffer (ReadToEnd hangs on >10 KiB piped stdin): %s", script)
	}
	if strings.Contains(script, "ReadToEnd()") {
		t.Fatalf("psWriteFileRawScript must NOT use [Console]::In.ReadToEnd() (hangs on >10 KiB piped stdin via SSH): %s", script)
	}
	if !strings.Contains(script, `$ms.ToArray()`) {
		t.Fatalf("psWriteFileRawScript must hand $ms.ToArray() (byte[]) to WriteAllBytes — passing ReadToEnd() (String) would be a type error: %s", script)
	}
	if !strings.Contains(script, `'C:\Users\esthie\data.bin'`) {
		t.Fatalf("psWriteFileRawScript should embed the path as a single-quoted literal: %s", script)
	}
	if strings.Contains(script, ".Trim()") {
		t.Fatalf("psWriteFileRawScript must NOT trim — raw file bytes may legitimately end in whitespace/newline, trimming would corrupt them: %s", script)
	}
	if !strings.Contains(script, "New-Item -ItemType Directory") {
		t.Fatalf("psWriteFileRawScript should create parent dirs, matching psWriteFileScript: %s", script)
	}
}

// TestPSWriteFileScriptsAreDistinct ensures the two scripts don't accidentally
// alias one to the other — a refactor that pointed both call sites at the same
// function would silently break either the inline or the source-stream path.
func TestPSWriteFileScriptsAreDistinct(t *testing.T) {
	path := `C:\x\y.dat`
	base := psWriteFileScript(path)
	raw := psWriteFileRawScript(path)
	if base == raw {
		t.Fatalf("psWriteFileScript and psWriteFileRawScript must be distinct (one decodes base64, the other writes raw): %s", base)
	}
}

// TestPSKeyAppendScript guards the authorized_keys append path. The key is
// shipped over the same SSH stdin pipe as write_file content, so it inherits
// the same ReadToEnd-hangs-on->10KiB bug. The fix is the same
// [Console]::OpenStandardInput() + Stream.Read loop, with the Trim applied
// to the decoded String (key is known text, trailing newline is transport
// artifact) — NOT to the raw bytes, which would corrupt a key whose final
// segment legitimately ended in whitespace.
func TestPSKeyAppendScript(t *testing.T) {
	script := psKeyAppendScript()
	if !strings.Contains(script, "Add-Content") {
		t.Fatalf("psKeyAppendScript should append to authorized_keys: %s", script)
	}
	if !strings.Contains(script, "OpenStandardInput") {
		t.Fatalf("psKeyAppendScript must stream stdin via OpenStandardInput (ReadToEnd hangs on >10 KiB piped stdin): %s", script)
	}
	if !strings.Contains(script, "$s.Read(") {
		t.Fatalf("psKeyAppendScript must read via a Stream.Read loop into a buffer (ReadToEnd hangs on >10 KiB piped stdin): %s", script)
	}
	if strings.Contains(script, "ReadToEnd()") {
		t.Fatalf("psKeyAppendScript must NOT use [Console]::In.ReadToEnd() (hangs on >10 KiB piped stdin via SSH): %s", script)
	}
	if !strings.Contains(script, ".Trim()") {
		t.Fatalf("psKeyAppendScript should Trim the decoded key (text format, trailing newline is transport artifact): %s", script)
	}
	if !strings.Contains(script, "authorized_keys") {
		t.Fatalf("psKeyAppendScript should target authorized_keys: %s", script)
	}
}

func TestMachineKeyAppendRemote(t *testing.T) {
	key := "ssh-ed25519 AAAAC3 test@host"

	posix := &machines.Machine{Name: "box", Shell: machines.ShellPosix}
	remote, stdin := machineKeyAppendRemote(posix, key)
	if len(remote) != 1 || !strings.Contains(remote[0], "~/.ssh/authorized_keys") || strings.Contains(remote[0], "/root/.ssh") {
		t.Fatalf("posix key append should target ~/.ssh, got: %v", remote)
	}
	if strings.TrimSpace(string(stdin)) != key {
		t.Fatalf("posix key stdin: %q", stdin)
	}

	win := &machines.Machine{Name: "win", Shell: machines.ShellPowerShell}
	remote, stdin = machineKeyAppendRemote(win, key)
	if len(remote) != 4 || remote[2] != "-EncodedCommand" {
		t.Fatalf("powershell key append shape: %v", remote)
	}
	script := decodeEncodedCommand(t, remote[3])
	if !strings.Contains(script, "USERPROFILE") || !strings.Contains(script, "authorized_keys") {
		t.Fatalf("powershell key script: %s", script)
	}
	if strings.TrimSpace(string(stdin)) != key {
		t.Fatalf("powershell key stdin: %q", stdin)
	}
}
