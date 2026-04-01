package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.ekaterina.net/administrator/human-relay/store"
)

func TestExecuteScript(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "hello.sh", `#!/bin/bash
echo "hello world"
`)

	e := newTestExecutor()
	// Override the scripts dir for testing
	origDir := defaultScriptsDir
	// We can't easily override the const, so we test via a real script path
	// by creating the request with the full path approach.
	// Actually, let's test ExecuteScript which uses defaultScriptsDir.
	// We need to work around the const. Let's test with a symlink or
	// just test the executor directly by creating scripts in /tmp.
	_ = origDir

	// Create a temp scripts dir and a script in it
	scriptPath := filepath.Join(dir, "hello.sh")

	req := &store.Request{
		Type:       "script",
		ScriptName: "hello",
		Timeout:    10,
	}

	// We need to override defaultScriptsDir for tests.
	// Since it's a const, let's use a different approach:
	// test ExecuteScriptIn which takes a dir parameter.
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello world") {
		t.Fatalf("expected 'hello world' in stdout, got: %s", result.Stdout)
	}
	_ = scriptPath
}

func TestExecuteScriptNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "fail.sh", `#!/bin/bash
echo "oops" >&2
exit 42
`)

	e := newTestExecutor()
	req := &store.Request{
		Type:       "script",
		ScriptName: "fail",
		Timeout:    10,
	}

	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "oops") {
		t.Fatalf("expected 'oops' in stderr, got: %s", result.Stderr)
	}
}

func TestExecuteScriptNotFound(t *testing.T) {
	dir := t.TempDir()
	e := newTestExecutor()

	req := &store.Request{
		Type:       "script",
		ScriptName: "nonexistent",
		Timeout:    10,
	}

	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for missing script")
	}
}

func TestExecuteScriptInheritsEnv(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "env-check.sh", `#!/bin/bash
echo "key=$TEST_SCRIPT_VAR"
`)

	os.Setenv("TEST_SCRIPT_VAR", "secret123")
	defer os.Unsetenv("TEST_SCRIPT_VAR")

	e := newTestExecutor()
	req := &store.Request{
		Type:       "script",
		ScriptName: "env-check",
		Timeout:    10,
	}

	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "key=secret123") {
		t.Fatalf("expected env var in output, got: %s", result.Stdout)
	}
}

func TestExecuteScriptTimeout(t *testing.T) {
	dir := t.TempDir()
	// Use a trap to handle SIGTERM and a short sleep loop so the process
	// can be killed cleanly by context cancellation.
	writeScript(t, dir, "slow.sh", `#!/bin/bash
trap 'exit 1' TERM
while true; do sleep 0.1; done
`)

	e := New(Config{DefaultTimeout: 1, MaxTimeout: 2})
	req := &store.Request{
		Type:       "script",
		ScriptName: "slow",
		Timeout:    1,
	}

	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != -1 {
		t.Fatalf("expected exit code -1 for timeout, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "timed out") {
		t.Fatalf("expected timeout message in stderr, got: %s", result.Stderr)
	}
}

func TestExecuteScriptCreate(t *testing.T) {
	dir := t.TempDir()
	e := newTestExecutor()

	content := `#!/bin/bash
echo "new script"
`
	req := &store.Request{
		Type:       "script_create",
		ScriptName: "new-script",
		Stdin:      []byte(content),
	}

	result := e.ExecuteScriptCreate(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}

	// Verify file was written
	written, err := os.ReadFile(filepath.Join(dir, "new-script.sh"))
	if err != nil {
		t.Fatalf("script file not created: %v", err)
	}
	if string(written) != content {
		t.Fatalf("script content mismatch: got %q", string(written))
	}

	// Verify executable permission
	info, _ := os.Stat(filepath.Join(dir, "new-script.sh"))
	if info.Mode()&0111 == 0 {
		t.Fatal("script should be executable")
	}
}

func TestExecuteScriptCreateOverwrite(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, dir, "existing.sh", "#!/bin/bash\necho old\n")

	e := newTestExecutor()
	newContent := "#!/bin/bash\necho new\n"

	req := &store.Request{
		Type:       "script_create",
		ScriptName: "existing",
		Stdin:      []byte(newContent),
	}

	result := e.ExecuteScriptCreate(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}

	written, _ := os.ReadFile(filepath.Join(dir, "existing.sh"))
	if string(written) != newContent {
		t.Fatalf("expected overwrite, got: %q", string(written))
	}
}

func writeScript(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("write script %s: %v", name, err)
	}
}
