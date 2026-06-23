package mcp

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/standardnguyen/human-relay/machines"
)

// This file holds the per-shell command construction for machine targets.
// Containers are always posix; machines can be posix OR powershell (Windows).
// The whole point of the abstraction is that exec/write_file/key-install
// generate correct remote syntax for whichever shell the machine runs, so a
// Windows box is a first-class target rather than a half-citizen.
//
// PowerShell construction avoids ssh<->shell quoting hell two ways:
//   - command scripts are passed as `-EncodedCommand <base64-UTF16LE>`, so the
//     literal script (including any embedded path) never gets re-parsed on the
//     wire. The dashboard decodes these for the reviewer (web/templates,
//     2026-05-30), so the approval pane still shows the real command.
//   - file/key bytes flow over ssh stdin (base64 for files), never on the
//     command line — same philosophy as the posix `cat > file` path.

// pwshQuote wraps s as a single-quoted PowerShell string literal. In single
// quotes PowerShell treats everything literally except a single quote, which is
// escaped by doubling it.
func pwshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psEncodedCommand encodes a PowerShell script as base64 of its UTF-16LE bytes,
// suitable for `powershell -EncodedCommand`. This is the canonical way to pass a
// non-trivial script to PowerShell without quoting it through an outer shell.
func psEncodedCommand(script string) string {
	u := utf16.Encode([]rune(script))
	buf := make([]byte, len(u)*2)
	for i, r := range u {
		buf[i*2] = byte(r)
		buf[i*2+1] = byte(r >> 8)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// psInvoke returns the argv that runs an -EncodedCommand script on a remote
// Windows host. -NoProfile keeps startup fast and deterministic.
func psInvoke(script string) []string {
	return []string{"powershell", "-NoProfile", "-EncodedCommand", psEncodedCommand(script)}
}

// joinCmd concatenates a command and its args into a single shell string.
func joinCmd(command string, cmdArgs []string) string {
	full := command
	for _, a := range cmdArgs {
		full += " " + a
	}
	return full
}

// machineExecRemote returns the remote-command args to append after
// "user@host --" for running command+args on a machine, honoring its shell type
// and the shell flag.
//
//   - posix: identical to exec_container — separate argv (shell=false) or one
//     joined string the remote login shell parses (shell=true).
//   - powershell, shell=false: pass the binary + args through; the remote default
//     shell launches the binary (works for plain invocations like `docker ps`).
//   - powershell, shell=true: run the joined string through powershell via
//     -EncodedCommand so pipes/cmdlets/quoting all work regardless of the host's
//     configured default ssh shell (which on Windows is usually cmd.exe).
func machineExecRemote(m *machines.Machine, command string, cmdArgs []string, shell bool) []string {
	if m.Shell == machines.ShellPowerShell {
		if shell {
			return psInvoke(joinCmd(command, cmdArgs))
		}
		return append([]string{command}, cmdArgs...)
	}
	// posix
	if shell {
		return []string{joinCmd(command, cmdArgs)}
	}
	return append([]string{command}, cmdArgs...)
}

// psWriteFileScript builds the PowerShell script that reads base64 content from
// stdin and writes the decoded bytes to path (creating parent dirs). Binary-safe
// and quote-safe: the path is embedded as a single-quoted literal inside the
// encoded script, so it never transits the ssh command line unquoted.
//
// IMPORTANT: stdin is read via [Console]::OpenStandardInput() + a 8 KiB
// Stream.Read loop into a MemoryStream. We do NOT use [Console]::In.ReadToEnd()
// — that path blocks indefinitely on Windows PowerShell when the stdin stream
// is a pipe (the SSH transport) and the content is >10 KiB, because the
// underlying TextReader waits for an EOF that never arrives while the pipe is
// still being written to. The 8 KiB read loop returns 0 on EOF and the loop
// exits cleanly. Bug #1 — fixed.
func psWriteFileScript(path string) string {
	return strings.Join([]string{
		"$ErrorActionPreference='Stop'",
		"$ms=New-Object IO.MemoryStream",
		"$s=[Console]::OpenStandardInput()",
		"$buf=New-Object byte[] 8192",
		"while(($n=$s.Read($buf,0,8192)) -gt 0){$ms.Write($buf,0,$n)}",
		"$b=[Text.Encoding]::ASCII.GetString($ms.ToArray())",
		"$bytes=[Convert]::FromBase64String($b)",
		"$path=" + pwshQuote(path),
		"$dir=Split-Path -Parent $path",
		"if($dir){[void](New-Item -ItemType Directory -Force -Path $dir)}",
		"[IO.File]::WriteAllBytes($path,$bytes)",
	}, "; ")
}

// psWriteFileRawScript is the source-stream counterpart to psWriteFileScript.
// When write_file uses source_path, the relay pipes raw bytes from a remote
// `cat` over SSH directly into the destination — no base64 round-trip on the
// relay side. The remote script must therefore write stdin verbatim, NOT
// FromBase64String-decode it. File bytes can legitimately end in whitespace
// or newlines, so we must NOT trim (cf. psKeyAppendScript, where the key is a
// known text format and Trim is appropriate). Parent-dir creation matches
// psWriteFileScript for consistency.
//
// IMPORTANT: stdin is read via the same [Console]::OpenStandardInput() +
// 8 KiB Stream.Read loop as psWriteFileScript. The bytes are written via
// [IO.File]::WriteAllBytes($path, $ms.ToArray()) — $ms.ToArray() is byte[],
// which is what WriteAllBytes expects. Bug #1 — fixed (the previous version
// captured [Console]::In.ReadToEnd() (String) and passed it to WriteAllBytes
// (wants byte[]) — type error that would have either errored or corrupted
// binary content).
func psWriteFileRawScript(path string) string {
	return strings.Join([]string{
		"$ErrorActionPreference='Stop'",
		"$ms=New-Object IO.MemoryStream",
		"$s=[Console]::OpenStandardInput()",
		"$buf=New-Object byte[] 8192",
		"while(($n=$s.Read($buf,0,8192)) -gt 0){$ms.Write($buf,0,$n)}",
		"$path=" + pwshQuote(path),
		"$dir=Split-Path -Parent $path",
		"if($dir){[void](New-Item -ItemType Directory -Force -Path $dir)}",
		"[IO.File]::WriteAllBytes($path,$ms.ToArray())",
	}, "; ")
}

// machineWriteRemote returns (remoteArgs, stdin) for writing content to path on
// a machine. posix pipes raw content through `cat`; powershell base64-encodes the
// content into stdin and decodes it on the far side. `mode` is honored for posix
// (chmod) and ignored for powershell (Windows uses ACLs, not octal modes).
func machineWriteRemote(m *machines.Machine, path, mode string, content []byte) (remote []string, stdin []byte) {
	if m.Shell == machines.ShellPowerShell {
		b64 := base64.StdEncoding.EncodeToString(content)
		return psInvoke(psWriteFileScript(path)), []byte(b64)
	}
	shellCmd := fmt.Sprintf("cat > %s && chmod %s %s", shellQuote(path), mode, shellQuote(path))
	return []string{shellCmd}, content
}

// psKeyAppendScript appends an authorized key (read from stdin) to the login
// user's profile authorized_keys. This is correct for a non-admin service user —
// the recommended Windows OpenSSH setup. Admin-group users need
// %ProgramData%\ssh\administrators_authorized_keys with restricted ACLs instead;
// that case is documented rather than auto-handled.
//
// IMPORTANT: stdin is read via the same [Console]::OpenStandardInput() +
// 8 KiB Stream.Read loop as psWriteFileScript. The Trim is applied to the
// decoded string (the key is known text and a trailing newline is a transport
// artifact), NOT to the raw bytes — Trim on the byte stream would corrupt a
// key whose final segment happened to end in whitespace.
func psKeyAppendScript() string {
	return strings.Join([]string{
		"$ErrorActionPreference='Stop'",
		"$ms=New-Object IO.MemoryStream",
		"$s=[Console]::OpenStandardInput()",
		"$buf=New-Object byte[] 8192",
		"while(($n=$s.Read($buf,0,8192)) -gt 0){$ms.Write($buf,0,$n)}",
		"$k=[Text.Encoding]::ASCII.GetString($ms.ToArray()).Trim()",
		"$d=Join-Path $env:USERPROFILE '.ssh'",
		"[void](New-Item -ItemType Directory -Force -Path $d)",
		"Add-Content -Path (Join-Path $d 'authorized_keys') -Value $k",
	}, "; ")
}

// machineKeyAppendRemote returns (remoteArgs, stdin) for appending an SSH public
// key to a machine's authorized_keys. posix uses ~/.ssh (not /root/.ssh — a
// machine's login user is arbitrary); powershell uses the user profile.
func machineKeyAppendRemote(m *machines.Machine, key string) (remote []string, stdin []byte) {
	if m.Shell == machines.ShellPowerShell {
		return psInvoke(psKeyAppendScript()), []byte(sshKeyLine(key))
	}
	shellCmd := "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && chmod 700 ~/.ssh"
	return []string{shellCmd}, []byte(sshKeyLine(key))
}
