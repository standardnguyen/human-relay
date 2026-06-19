package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

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
