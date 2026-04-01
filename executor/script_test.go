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

func TestExecuteScriptNotFound(t *testing.T) {
	dir := t.TempDir()
	e := newTestExecutor()
	req := &store.Request{Type: "script", ScriptName: "nonexistent", Timeout: 10}
	result := e.ExecuteScriptIn(req, dir)

	if result.ExitCode == 0 {
		t.Fatal("expected non-zero exit code for missing script")
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
	if !strings.Contains(result.Stderr, "parse") {
		t.Fatalf("expected parse error, got: %s", result.Stderr)
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
