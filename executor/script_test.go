package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.ekaterina.net/administrator/human-relay/store"
)

func TestExecuteScriptPipeline(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":"c1","name":"Buy milk"}]`)
	}))
	defer ts.Close()

	dir := t.TempDir()
	p := Pipeline{
		Steps: []Step{
			{Method: "GET", URL: ts.URL + "/cards", Extract: map[string]string{"name": "0.name"}},
		},
		Output: "Got: ${name}",
	}
	writeJSON(t, dir, "test", &p)

	e := newTestExecutor()
	req := &store.Request{Type: "script", ScriptName: "test", Timeout: 10}
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Got: Buy milk") {
		t.Fatalf("expected output, got: %s", result.Stdout)
	}
}

func TestExecuteScriptPython(t *testing.T) {
	dir := t.TempDir()
	writePy(t, dir, "hello", `import os
print("hello from python")
print(f"var={os.environ.get('TEST_PY_VAR', 'unset')}")
`)

	os.Setenv("TEST_PY_VAR", "works")
	defer os.Unsetenv("TEST_PY_VAR")

	e := newTestExecutor()
	req := &store.Request{Type: "script", ScriptName: "hello", Timeout: 10}
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello from python") {
		t.Fatalf("expected python output, got: %s", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "var=works") {
		t.Fatalf("expected env var, got: %s", result.Stdout)
	}
}

func TestExecuteScriptPythonNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	writePy(t, dir, "fail", `import sys
print("oops", file=sys.stderr)
sys.exit(42)
`)

	e := newTestExecutor()
	req := &store.Request{Type: "script", ScriptName: "fail", Timeout: 10}
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "oops") {
		t.Fatalf("expected stderr, got: %s", result.Stderr)
	}
}

func TestExecuteScriptPyTakesPrecedence(t *testing.T) {
	// When both .py and .json exist, .py wins
	dir := t.TempDir()
	writePy(t, dir, "both", `print("from python")`)
	p := Pipeline{Steps: []Step{}, Output: "from json"}
	writeJSON(t, dir, "both", &p)

	e := newTestExecutor()
	req := &store.Request{Type: "script", ScriptName: "both", Timeout: 10}
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "from python") {
		t.Fatalf("expected python to take precedence, got: %s", result.Stdout)
	}
}

func TestExecuteScriptNotFound(t *testing.T) {
	dir := t.TempDir()
	e := newTestExecutor()
	req := &store.Request{Type: "script", ScriptName: "nonexistent", Timeout: 10}
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for missing script")
	}
	if !strings.Contains(result.Stderr, "script not found") {
		t.Fatalf("expected 'script not found', got: %s", result.Stderr)
	}
}

func TestExecuteScriptInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("not json"), 0644)

	e := newTestExecutor()
	req := &store.Request{Type: "script", ScriptName: "bad", Timeout: 10}
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for invalid JSON")
	}
}

func TestExecuteScriptTimeout(t *testing.T) {
	dir := t.TempDir()
	writePy(t, dir, "slow", `import time
time.sleep(30)
`)

	e := New(Config{DefaultTimeout: 1, MaxTimeout: 2})
	req := &store.Request{Type: "script", ScriptName: "slow", Timeout: 1}
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode != -1 {
		t.Fatalf("expected exit code -1 for timeout, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "timed out") {
		t.Fatalf("expected timeout message, got: %s", result.Stderr)
	}
}

func TestExecuteScriptCreate(t *testing.T) {
	dir := t.TempDir()
	e := newTestExecutor()

	content := `{"steps":[],"output":"hello"}`
	req := &store.Request{
		Type:       "script_create",
		ScriptName: "new-script",
		Stdin:      []byte(content),
	}

	result := e.ExecuteScriptCreate(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}

	written, err := os.ReadFile(filepath.Join(dir, "new-script.json"))
	if err != nil {
		t.Fatalf("script file not created: %v", err)
	}
	if string(written) != content {
		t.Fatalf("content mismatch: got %q", string(written))
	}
}

func TestExecuteScriptCreatePython(t *testing.T) {
	dir := t.TempDir()
	e := newTestExecutor()

	content := `import os
print("hello")
`
	req := &store.Request{
		Type:       "script_create",
		ScriptName: "my-script",
		Stdin:      []byte(content),
	}

	result := e.ExecuteScriptCreate(req, dir)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}

	// Should be saved as .py since it's not valid JSON
	written, err := os.ReadFile(filepath.Join(dir, "my-script.py"))
	if err != nil {
		t.Fatalf("python script not created: %v", err)
	}
	if string(written) != content {
		t.Fatalf("content mismatch: got %q", string(written))
	}

	// Should be executable
	info, _ := os.Stat(filepath.Join(dir, "my-script.py"))
	if info.Mode()&0111 == 0 {
		t.Fatal("python script should be executable")
	}
}

func TestExecuteScriptCreateOverwrite(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "existing.json"), []byte(`{"steps":[],"output":"old"}`), 0644)

	e := newTestExecutor()
	newContent := `{"steps":[],"output":"new"}`
	req := &store.Request{
		Type:       "script_create",
		ScriptName: "existing",
		Stdin:      []byte(newContent),
	}

	result := e.ExecuteScriptCreate(req, dir)
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}

	written, _ := os.ReadFile(filepath.Join(dir, "existing.json"))
	if string(written) != newContent {
		t.Fatalf("expected overwrite, got: %q", string(written))
	}
}

func TestDetectScriptType(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"json pipeline", `{"steps":[],"output":"hi"}`, ".json"},
		{"python", `import os\nprint("hi")`, ".py"},
		{"empty", "", ".py"},
		{"json array", `[1,2,3]`, ".py"}, // arrays aren't pipeline objects
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectScriptType([]byte(tt.content))
			if got != tt.want {
				t.Fatalf("detectScriptType(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

func writeJSON(t *testing.T, dir, name string, p *Pipeline) {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal pipeline: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0644); err != nil {
		t.Fatalf("write pipeline %s: %v", name, err)
	}
}

func writePy(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".py"), []byte(content), 0755); err != nil {
		t.Fatalf("write python %s: %v", name, err)
	}
}
