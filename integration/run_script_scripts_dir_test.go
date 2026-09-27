package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunScriptRunsFromConfiguredScriptsDir pins "run_script runs from
// /scripts whatever MHR_SCRIPTS_DIR says": run_script checked that the script
// existed in the configured directory, but the executor the approval handler
// dispatched to read a hard-coded /scripts, so the script that was accepted
// was never the one that ran. With MHR_SCRIPTS_DIR pointed elsewhere, the
// approved script must run from that directory.
func TestRunScriptRunsFromConfiguredScriptsDir(t *testing.T) {
	const name = "scripts-dir-probe"
	const marker = "ran-from-configured-dir"

	scriptsDir := t.TempDir()
	body := "#!/bin/sh\necho \"" + marker + ":$1\"\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, name+".sh"), []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	wlPath := filepath.Join(t.TempDir(), "whitelist.json")
	rules, _ := json.Marshal([]map[string]interface{}{
		{"command": "run_script", "args": []string{name}},
	})
	if err := os.WriteFile(wlPath, rules, 0644); err != nil {
		t.Fatalf("write whitelist: %v", err)
	}

	s := StartServer(t, WithWhitelistFile(wlPath), WithScriptsDir(scriptsDir))
	c := NewMCPClient(t, s.MCPURL())
	c.Call(t, 1, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "test", "version": "1.0"},
	})
	c.Notify(t, "notifications/initialized", nil)

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "run_script",
		"arguments": map[string]interface{}{
			"name":   name,
			"args":   []string{"arg1"},
			"reason": "run_script under MHR_SCRIPTS_DIR",
		},
	})
	id := extractRequestID(t, resp)

	result := pollUntilDone(t, c, 3, id)
	if result.Result == nil {
		t.Fatalf("request %s never produced a result (status %s)", id, result.Status)
	}
	if result.Status != "complete" {
		t.Fatalf("run_script of a script in MHR_SCRIPTS_DIR=%s ended %s (exit %d); the executor did not run it from the configured directory. stderr: %q",
			scriptsDir, result.Status, result.Result.ExitCode, result.Result.Stderr)
	}
	if want := marker + ":arg1"; !strings.Contains(result.Result.Stdout, want) {
		t.Errorf("stdout = %q, want it to contain %q", result.Result.Stdout, want)
	}
}
