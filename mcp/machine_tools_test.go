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

// registerWin seeds the machine registry directly — register_machine is
// approval-gated (finding #23 part 2) and only queues a request, so tests that
// need a registered Windows machine as a precondition bypass the tool.
func registerWin(t *testing.T, h *ToolHandler) {
	t.Helper()
	seedMachine(t, h, "corsair-win", "100.106.181.59", "esthie", machines.ShellPowerShell)
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
			if res := h.Handle("register_machine", tc.args, ""); !res.IsError {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestRegisterListDeleteMachine(t *testing.T) {
	h := setup(t)

	// register_machine queues rather than writing the registry (finding #23 part 2).
	regRes := h.Handle("register_machine", map[string]interface{}{
		"name":     "corsair-win",
		"host":     "100.106.181.59",
		"ssh_user": "esthie",
		"shell":    "powershell",
	}, "")
	regReq := assertRegistryOpQueued(t, h, regRes, "register_machine")
	for k, want := range map[string]string{
		"name": "corsair-win", "host": "100.106.181.59",
		"ssh_user": "esthie", "shell": "powershell",
	} {
		if got := regReq.RegistryArgs[k]; got != want {
			t.Errorf("registry_args[%q]: expected %q, got %q", k, want, got)
		}
	}
	if listRes := h.Handle("list_machines", map[string]interface{}{}, ""); !strings.Contains(listRes.Content[0].Text, "[]") {
		t.Fatalf("registry mutated before approval: %s", listRes.Content[0].Text)
	}

	// Seed directly so list + delete have something to work with.
	registerWin(t, h)

	listRes := h.Handle("list_machines", map[string]interface{}{}, "")
	var list []machines.Machine
	if err := json.Unmarshal([]byte(listRes.Content[0].Text), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "corsair-win" || list[0].Shell != machines.ShellPowerShell {
		t.Fatalf("unexpected list: %+v", list)
	}

	delRes := h.Handle("delete_machine", map[string]interface{}{"name": "corsair-win"}, "")
	delReq := assertRegistryOpQueued(t, h, delRes, "delete_machine")
	if delReq.RegistryArgs["name"] != "corsair-win" {
		t.Fatalf("expected name corsair-win in registry_args, got %+v", delReq.RegistryArgs)
	}
	listRes = h.Handle("list_machines", map[string]interface{}{}, "")
	if strings.Contains(listRes.Content[0].Text, "[]") {
		t.Fatalf("machine removed before approval: %s", listRes.Content[0].Text)
	}
}

func TestExecMachineNotFound(t *testing.T) {
	h := setup(t)
	res := h.Handle("exec_machine", map[string]interface{}{
		"machine": "ghost", "command": "ls", "reason": "x",
	}, "")
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
	}, "")
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
	seedMachine(t, h, "wsl", "100.106.181.59", "gpu", machines.ShellPosix)
	res := h.Handle("exec_machine", map[string]interface{}{
		"machine": "wsl", "command": "nvidia-smi", "reason": "gpu check",
	}, "")
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
	}, "")
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
	}, "")
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
	}, "")
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
	}, "")
	if !res.IsError || !strings.Contains(res.Content[0].Text, "ctid or machine") {
		t.Fatalf("expected ctid-or-machine error, got %+v", res)
	}
}

func TestDeleteContainer(t *testing.T) {
	h := setup(t)
	seedContainer(t, h, 9104, "100.106.181.59", "gpu-worker", true)

	del := h.Handle("delete_container", map[string]interface{}{"ctid": float64(9104)}, "")
	delReq := assertRegistryOpQueued(t, h, del, "delete_container")
	if delReq.RegistryArgs["ctid"] != "9104" {
		t.Fatalf("expected ctid 9104 in registry_args, got %+v", delReq.RegistryArgs)
	}

	// Still registered: the delete only lands once a human approves. Deleting an
	// unregistered CTID likewise only fails at execution time — covered in the
	// integration suite, which can drive the approval.
	list := h.Handle("list_containers", map[string]interface{}{}, "")
	if !strings.Contains(list.Content[0].Text, "9104") {
		t.Fatalf("container removed before approval: %s", list.Content[0].Text)
	}

	if mc := h.Handle("delete_container", map[string]interface{}{}, ""); !mc.IsError {
		t.Fatal("expected error for missing ctid")
	}
}
