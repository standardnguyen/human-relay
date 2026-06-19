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
