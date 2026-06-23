package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/machines"
)

// reqFromResult extracts the request_id from a tool result and returns the
// stored request so tests can inspect the constructed SSH command and stdin.
func reqFromResult(t *testing.T, h *ToolHandler, result *CallToolResult) (string, []string, []byte) {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	var out struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	r := h.store.Get(out.RequestID)
	if r == nil {
		t.Fatalf("request %s not found in store", out.RequestID)
	}
	return r.Reason, r.Args, r.Stdin
}

// reqFullCommand extracts the full command string from a tool result, joining
// Command and Args. Source-stream writes use Shell=true with the full pipeline
// in Command, so inspecting r.Args alone (which reqFromResult returns) gives
// an empty slice for those requests. This helper returns the joined string
// the way the integration tests inspect it.
func reqFullCommand(t *testing.T, h *ToolHandler, result *CallToolResult) string {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	var out struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	r := h.store.Get(out.RequestID)
	if r == nil {
		t.Fatalf("request %s not found in store", out.RequestID)
	}
	full := r.Command
	if len(r.Args) > 0 {
		full += " " + strings.Join(r.Args, " ")
	}
	return full
}

func registerWin(t *testing.T, h *ToolHandler) {
	t.Helper()
	res := h.Handle("register_machine", map[string]interface{}{
		"name":     "corsair-win",
		"host":     "100.106.181.59",
		"ssh_user": "esthie",
		"shell":    "powershell",
	})
	if res.IsError {
		t.Fatalf("register_machine: %s", res.Content[0].Text)
	}
}

func TestRegisterMachineValidation(t *testing.T) {
	h := setup(t)
	cases := []struct {
		name string
		args map[string]interface{}
	}{
		{"missing name", map[string]interface{}{"host": "1.2.3.4", "ssh_user": "x"}},
		{"missing host", map[string]interface{}{"name": "a", "ssh_user": "x"}},
		{"missing user", map[string]interface{}{"name": "a", "host": "1.2.3.4"}},
		{"bad name", map[string]interface{}{"name": "a/b", "host": "1.2.3.4", "ssh_user": "x"}},
		{"bad shell", map[string]interface{}{"name": "a", "host": "1.2.3.4", "ssh_user": "x", "shell": "fish"}},
		{"bad identity", map[string]interface{}{"name": "a", "host": "1.2.3.4", "ssh_user": "x", "identity_file": "relative"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if res := h.Handle("register_machine", tc.args); !res.IsError {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestRegisterListDeleteMachine(t *testing.T) {
	h := setup(t)
	registerWin(t, h)

	listRes := h.Handle("list_machines", map[string]interface{}{})
	var list []machines.Machine
	if err := json.Unmarshal([]byte(listRes.Content[0].Text), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "corsair-win" || list[0].Shell != machines.ShellPowerShell {
		t.Fatalf("unexpected list: %+v", list)
	}

	delRes := h.Handle("delete_machine", map[string]interface{}{"name": "corsair-win"})
	if delRes.IsError {
		t.Fatalf("delete: %s", delRes.Content[0].Text)
	}
	listRes = h.Handle("list_machines", map[string]interface{}{})
	if !strings.Contains(listRes.Content[0].Text, "[]") {
		t.Fatalf("expected empty list, got %s", listRes.Content[0].Text)
	}
}

func TestExecMachineNotFound(t *testing.T) {
	h := setup(t)
	res := h.Handle("exec_machine", map[string]interface{}{
		"machine": "ghost", "command": "ls", "reason": "x",
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "not found") {
		t.Fatalf("expected not-found error, got %+v", res)
	}
}

func TestExecMachinePowerShell(t *testing.T) {
	h := setup(t)
	registerWin(t, h)

	// shell=true → ssh -F? user@host -- powershell -NoProfile -EncodedCommand <b64>
	res := h.Handle("exec_machine", map[string]interface{}{
		"machine": "corsair-win",
		"command": "Get-Process",
		"reason":  "check procs",
		"shell":   true,
	})
	reason, args, _ := reqFromResult(t, h, res)
	if !strings.Contains(reason, "[MACHINE corsair-win (powershell)]") {
		t.Fatalf("reason prefix wrong: %s", reason)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "esthie@100.106.181.59") {
		t.Fatalf("missing user@host: %v", args)
	}
	if !strings.Contains(joined, "powershell -NoProfile -EncodedCommand") {
		t.Fatalf("missing powershell invoke: %v", args)
	}
}

func TestExecMachinePosixDirectArgs(t *testing.T) {
	h := setup(t)
	h.Handle("register_machine", map[string]interface{}{
		"name": "wsl", "host": "100.106.181.59", "ssh_user": "gpu", "shell": "posix",
	})
	res := h.Handle("exec_machine", map[string]interface{}{
		"machine": "wsl", "command": "nvidia-smi", "reason": "gpu check",
	})
	_, args, _ := reqFromResult(t, h, res)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "gpu@100.106.181.59 -- nvidia-smi") {
		t.Fatalf("posix exec args wrong: %v", args)
	}
}

func TestWriteFileMachinePowerShell(t *testing.T) {
	h := setup(t)
	registerWin(t, h)

	content := "line1\nline2"
	res := h.Handle("write_file", map[string]interface{}{
		"machine": "corsair-win",
		"path":    `C:\Users\esthie\note.txt`,
		"content": content,
		"reason":  "deploy note",
	})
	_, args, stdin := reqFromResult(t, h, res)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "esthie@100.106.181.59") || !strings.Contains(joined, "-EncodedCommand") {
		t.Fatalf("powershell write args wrong: %v", args)
	}
	// stdin is base64 of the content (decoded on the far side).
	if string(stdin) != base64.StdEncoding.EncodeToString([]byte(content)) {
		t.Fatalf("powershell write stdin should be base64, got %q", stdin)
	}
}

// TestWriteFileSourceToPosixMachine exercises the source-stream → posix
// machine branch. The destination is a registered posix machine, not a
// container or the Proxmox host. Stdout bytes from the source `cat` flow
// through the pipeline into the machine's `cat > <path> && chmod <mode>
// <path>` dest command. The probe is stubbed: posix machine writes do not
// run the existing dest probe (it SSHes as root@host, which doesn't match
// the machine's login user — see the inline-path precedent in tools.go:
// `machineName == ""` skips the probe at the inline write path).
func TestWriteFileSourceToPosixMachine(t *testing.T) {
	h := setup(t)
	h.SetWriteFileChecker(func(ctid int, host, path string, timeout time.Duration) (bool, int64, time.Time, error) {
		return true, 4096, time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC), nil
	})
	h.Handle("register_machine", map[string]interface{}{
		"name":     "wsl",
		"host":     "100.106.181.59",
		"ssh_user": "gpu",
		"shell":    "posix",
	})

	res := h.Handle("write_file", map[string]interface{}{
		"machine":     "wsl",
		"path":        "/tmp/dest.bin",
		"source_host": "10.0.0.1",
		"source_path": "/tmp/src.bin",
		"mode":        "0644",
		"reason":      "stream to posix machine",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content[0].Text)
	}
	reason, _, stdin := reqFromResult(t, h, res)
	if !strings.Contains(reason, "machine wsl") {
		t.Fatalf("reason should name the machine, got: %s", reason)
	}
	if !strings.Contains(reason, "10.0.0.1") || !strings.Contains(reason, "/tmp/src.bin") {
		t.Fatalf("reason should name the source, got: %s", reason)
	}
	// full command: ssh root@10.0.0.1 -- cat '/tmp/src.bin' | ssh gpu@100.106.181.59 -- "cat > '/tmp/dest.bin' && chmod 0644 '/tmp/dest.bin'"
	full := reqFullCommand(t, h, res)
	if !strings.Contains(full, "root@10.0.0.1") || !strings.Contains(full, "cat '/tmp/src.bin'") {
		t.Fatalf("source side wrong: %s", full)
	}
	if !strings.Contains(full, " | ") {
		t.Fatalf("expected pipeline separator, got: %s", full)
	}
	if !strings.Contains(full, "gpu@100.106.181.59") {
		t.Fatalf("dest must target the machine user@host, not root@<hostIP>: %s", full)
	}
	if !strings.Contains(full, "cat > '/tmp/dest.bin'") || !strings.Contains(full, "chmod 0644") {
		t.Fatalf("dest command shape wrong: %s", full)
	}
	// Bug being fixed: the dest must NOT be root@192.168.10.50 (the Proxmox host).
	if strings.Contains(full, "root@192.168.10.50") {
		t.Fatalf("dest silently misrouted to Proxmox host — machine destination is the bug: %s", full)
	}
	// Stdin must be empty: source-stream pipes bytes over the SSH pipeline, no
	// inline bytes via stdin.
	if len(stdin) != 0 {
		t.Fatalf("stdin should be empty (source-stream pipes through the pipeline), got %d bytes: %q", len(stdin), stdin)
	}
}

// TestWriteFileSourceToPowerShellMachine exercises the source-stream →
// powershell machine branch. The relay pipes raw bytes from a remote `cat`
// directly into the destination — no base64 round-trip. The dest script
// therefore must NOT call FromBase64String: that would try to base64-decode
// raw file bytes and corrupt them. This test would FAIL before the fix
// (writeFileFromSource had no machine branch and silently misrouted to the
// Proxmox host) and would also fail if we reused psWriteFileScript for the
// source-stream powershell path.
func TestWriteFileSourceToPowerShellMachine(t *testing.T) {
	h := setup(t)
	h.SetWriteFileChecker(func(ctid int, host, path string, timeout time.Duration) (bool, int64, time.Time, error) {
		return true, 4096, time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC), nil
	})
	registerWin(t, h)

	res := h.Handle("write_file", map[string]interface{}{
		"machine":     "corsair-win",
		"path":        `C:\Users\esthie\dest.bin`,
		"source_host": "10.0.0.1",
		"source_path": "/tmp/src.bin",
		"reason":      "stream to powershell machine",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content[0].Text)
	}
	reason, _, stdin := reqFromResult(t, h, res)
	if !strings.Contains(reason, "machine corsair-win") {
		t.Fatalf("reason should name the machine, got: %s", reason)
	}
	full := reqFullCommand(t, h, res)
	// Source side: ssh root@10.0.0.1 -- cat '/tmp/src.bin'
	if !strings.Contains(full, "root@10.0.0.1") || !strings.Contains(full, "cat '/tmp/src.bin'") {
		t.Fatalf("source side wrong: %s", full)
	}
	if !strings.Contains(full, " | ") {
		t.Fatalf("expected pipeline separator, got: %s", full)
	}
	// Dest side: ssh esthie@100.106.181.59 -- powershell -NoProfile -EncodedCommand <b64>
	if !strings.Contains(full, "esthie@100.106.181.59") {
		t.Fatalf("dest must target the machine user@host, not root@<hostIP>: %s", full)
	}
	if !strings.Contains(full, "powershell -NoProfile -EncodedCommand") {
		t.Fatalf("dest must invoke powershell via EncodedCommand, got: %s", full)
	}
	// Bug being fixed: the dest must NOT be root@192.168.10.50 (the Proxmox host).
	if strings.Contains(full, "root@192.168.10.50") {
		t.Fatalf("dest silently misrouted to Proxmox host — machine destination is the bug: %s", full)
	}
	// The encoded script is the last whitespace-separated token in the full
	// command (Command + Args). decodeEncodedCommand lives in
	// machine_shell_test.go and reverses psEncodedCommand (base64 →
	// UTF-16LE → string).
	tokens := strings.Fields(full)
	encoded := tokens[len(tokens)-1]
	script := decodeEncodedCommand(t, encoded)
	if strings.Contains(script, "FromBase64String") {
		t.Fatalf("source-stream powershell script must NOT base64-decode stdin (raw bytes from cat): %s", script)
	}
	if !strings.Contains(script, "WriteAllBytes") {
		t.Fatalf("source-stream powershell script should write bytes, got: %s", script)
	}
	// Streaming stdin read: ReadToEnd() hangs on >10 KiB piped stdin via SSH.
	// Must use [Console]::OpenStandardInput() + Stream.Read into MemoryStream,
	// and hand $ms.ToArray() (byte[]) to WriteAllBytes — ReadToEnd() would have
	// returned a String, which WriteAllBytes can't accept.
	if !strings.Contains(script, "OpenStandardInput") {
		t.Fatalf("source-stream powershell script must stream stdin via OpenStandardInput (ReadToEnd hangs on >10 KiB piped stdin): %s", script)
	}
	if !strings.Contains(script, "$s.Read(") {
		t.Fatalf("source-stream powershell script must read via a Stream.Read loop into a buffer (ReadToEnd hangs on >10 KiB piped stdin): %s", script)
	}
	if strings.Contains(script, "ReadToEnd()") {
		t.Fatalf("source-stream powershell script must NOT use [Console]::In.ReadToEnd() (hangs on >10 KiB piped stdin via SSH, AND returns String not byte[]): %s", script)
	}
	if !strings.Contains(script, "$ms.ToArray()") {
		t.Fatalf("source-stream powershell script must hand $ms.ToArray() (byte[]) to WriteAllBytes, got: %s", script)
	}
	if !strings.Contains(script, `'C:\Users\esthie\dest.bin'`) {
		t.Fatalf("source-stream powershell script should embed the path, got: %s", script)
	}
	// Stdin must be empty: source-stream pipes bytes through the pipeline,
	// not through the relay's stdin.
	if len(stdin) != 0 {
		t.Fatalf("stdin should be empty (source-stream pipes through the pipeline), got %d bytes: %q", len(stdin), stdin)
	}
}

// TestWriteFileSourceToUnknownMachine returns a clear error (not a silent
// misroute) when the machine isn't registered. Same precedent as the
// inline path's TestWriteFileMachineNotFound.
func TestWriteFileSourceToUnknownMachine(t *testing.T) {
	h := setup(t)
	res := h.Handle("write_file", map[string]interface{}{
		"machine":     "ghost",
		"path":        "/tmp/x",
		"source_host": "10.0.0.1",
		"source_path": "/tmp/src.bin",
		"reason":      "x",
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "not found") {
		t.Fatalf("expected not-found error, got: %+v", res)
	}
}

func TestWriteFileMachineNotFound(t *testing.T) {
	h := setup(t)
	res := h.Handle("write_file", map[string]interface{}{
		"machine": "ghost", "path": "/tmp/x", "content": "y", "reason": "z",
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "not found") {
		t.Fatalf("expected not-found, got %+v", res)
	}
}

func TestInstallSSHKeyMachine(t *testing.T) {
	h := setup(t)
	registerWin(t, h)

	res := h.Handle("install_ssh_key", map[string]interface{}{
		"machine":    "corsair-win",
		"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyData12345678901234567890123456 relay@host",
		"reason":     "grant relay access",
	})
	reason, args, stdin := reqFromResult(t, h, res)
	if !strings.Contains(reason, "machine corsair-win") {
		t.Fatalf("reason wrong: %s", reason)
	}
	if !strings.Contains(strings.Join(args, " "), "-EncodedCommand") {
		t.Fatalf("expected powershell key install, got %v", args)
	}
	if !strings.Contains(string(stdin), "ssh-ed25519") {
		t.Fatalf("key not in stdin: %q", stdin)
	}
}

func TestInstallSSHKeyRequiresTarget(t *testing.T) {
	h := setup(t)
	res := h.Handle("install_ssh_key", map[string]interface{}{
		"public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyData12345678901234567890123456 relay@host",
		"reason":     "x",
	})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "ctid or machine") {
		t.Fatalf("expected ctid-or-machine error, got %+v", res)
	}
}

func TestDeleteContainer(t *testing.T) {
	h := setup(t)
	h.Handle("register_container", map[string]interface{}{
		"ctid": float64(9104), "ip": "100.106.181.59", "hostname": "gpu-worker", "has_relay_ssh": true,
	})

	del := h.Handle("delete_container", map[string]interface{}{"ctid": float64(9104)})
	if del.IsError || !strings.Contains(del.Content[0].Text, "\"deleted\":true") {
		t.Fatalf("delete failed: %+v", del)
	}

	list := h.Handle("list_containers", map[string]interface{}{})
	if strings.Contains(list.Content[0].Text, "9104") {
		t.Fatalf("container still listed after delete: %s", list.Content[0].Text)
	}

	if again := h.Handle("delete_container", map[string]interface{}{"ctid": float64(9104)}); !again.IsError {
		t.Fatal("expected error deleting nonexistent container")
	}
	if mc := h.Handle("delete_container", map[string]interface{}{}); !mc.IsError {
		t.Fatal("expected error for missing ctid")
	}
}
