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
func psWriteFileScript(path string) string {
	return strings.Join([]string{
		"$ErrorActionPreference='Stop'",
		"$b=[Console]::In.ReadToEnd()",
		"$bytes=[Convert]::FromBase64String($b)",
		"$path=" + pwshQuote(path),
		"$dir=Split-Path -Parent $path",
		"if($dir){[void](New-Item -ItemType Directory -Force -Path $dir)}",
		"[IO.File]::WriteAllBytes($path,$bytes)",
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
func psKeyAppendScript() string {
	return strings.Join([]string{
		"$ErrorActionPreference='Stop'",
		"$k=[Console]::In.ReadToEnd().Trim()",
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
