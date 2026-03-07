package whitelist

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchExact(t *testing.T) {
	w := &Whitelist{
		rules: []Rule{
			{Command: "echo", Args: []string{"hello", "world"}},
			{Command: "ls", Args: []string{"-la"}},
		},
	}

	if !w.Match("echo", []string{"hello", "world"}) {
		t.Error("expected match for echo hello world")
	}
	if !w.Match("ls", []string{"-la"}) {
		t.Error("expected match for ls -la")
	}
}

func TestMatchNoArgs(t *testing.T) {
	w := &Whitelist{
		rules: []Rule{
			{Command: "uptime", Args: []string{}},
		},
	}

	if !w.Match("uptime", []string{}) {
		t.Error("expected match for uptime with empty args")
	}
	if !w.Match("uptime", nil) {
		t.Error("expected match for uptime with nil args")
	}
}

func TestNoMatchDifferentArgs(t *testing.T) {
	w := &Whitelist{
		rules: []Rule{
			{Command: "echo", Args: []string{"hello"}},
		},
	}

	if w.Match("echo", []string{"goodbye"}) {
		t.Error("should not match different args")
	}
	if w.Match("echo", []string{"hello", "extra"}) {
		t.Error("should not match extra args")
	}
	if w.Match("echo", nil) {
		t.Error("should not match nil args when rule has args")
	}
}

func TestNoMatchDifferentCommand(t *testing.T) {
	w := &Whitelist{
		rules: []Rule{
			{Command: "echo", Args: []string{"hello"}},
		},
	}

	if w.Match("cat", []string{"hello"}) {
		t.Error("should not match different command")
	}
}

func TestNoMatchEmptyWhitelist(t *testing.T) {
	w := &Whitelist{}

	if w.Match("echo", []string{"hello"}) {
		t.Error("empty whitelist should not match anything")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whitelist.json")

	err := os.WriteFile(path, []byte(`[
		{"command": "ssh", "args": ["root@192.168.10.93", "docker", "compose", "ps"]},
		{"command": "echo", "args": ["test"]}
	]`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	w, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if !w.Match("ssh", []string{"root@192.168.10.93", "docker", "compose", "ps"}) {
		t.Error("expected match for loaded rule")
	}
	if !w.Match("echo", []string{"test"}) {
		t.Error("expected match for second loaded rule")
	}
	if w.Match("echo", []string{"other"}) {
		t.Error("should not match non-loaded rule")
	}
}

func TestLoadMissingFile(t *testing.T) {
	w, err := Load("/nonexistent/whitelist.json")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if w.Match("anything", nil) {
		t.Error("missing file should produce empty whitelist")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whitelist.json")
	os.WriteFile(path, []byte(`not json`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestAddAndRemove(t *testing.T) {
	w := &Whitelist{}

	w.Add("echo", []string{"hello"})
	if !w.Match("echo", []string{"hello"}) {
		t.Error("expected match after Add")
	}

	// Duplicate add should be no-op
	w.Add("echo", []string{"hello"})
	if len(w.Rules()) != 1 {
		t.Errorf("expected 1 rule after duplicate add, got %d", len(w.Rules()))
	}

	removed := w.Remove("echo", []string{"hello"})
	if !removed {
		t.Error("expected Remove to return true")
	}
	if w.Match("echo", []string{"hello"}) {
		t.Error("should not match after Remove")
	}

	// Remove non-existent
	if w.Remove("echo", []string{"hello"}) {
		t.Error("expected Remove to return false for non-existent rule")
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "whitelist.json")

	w := &Whitelist{path: path}
	w.Add("echo", []string{"hello"})
	w.Add("ls", []string{"-la"})

	if err := w.Save(); err != nil {
		t.Fatal(err)
	}

	// Reload and verify
	w2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !w2.Match("echo", []string{"hello"}) {
		t.Error("expected match after save+load")
	}
	if !w2.Match("ls", []string{"-la"}) {
		t.Error("expected match after save+load")
	}
}

func TestRulesReturnsCopy(t *testing.T) {
	w := &Whitelist{
		rules: []Rule{{Command: "echo", Args: []string{"hello"}}},
	}

	rules := w.Rules()
	rules[0].Command = "modified"

	if !w.Match("echo", []string{"hello"}) {
		t.Error("modifying returned rules should not affect whitelist")
	}
}

func TestNilArgsMatchesEmptyArgs(t *testing.T) {
	w := &Whitelist{
		rules: []Rule{
			{Command: "uptime"},
		},
	}

	// Rule has nil Args (JSON omitted), request has empty slice
	if !w.Match("uptime", []string{}) {
		t.Error("nil rule args should match empty request args")
	}
	if !w.Match("uptime", nil) {
		t.Error("nil rule args should match nil request args")
	}
}
