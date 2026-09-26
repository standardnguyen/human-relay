package whitelist

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSkipsRemovedGateOutputRule pins the fail-closed load of a whitelist
// saved before the gate_output flag was removed. Decoding such a file into the
// current Rule would drop the field silently and turn "auto-approve, output
// withheld" into "auto-approve, output shown to the agent". A rule carrying
// gate_output must instead not load at all, so its requests wait for a human,
// and the skip must be logged by name without echoing the rule's arguments.
func TestLoadSkipsRemovedGateOutputRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whitelist.json")
	if err := os.WriteFile(path, []byte(`[
		{"command": "run_script", "args": ["signal-read"], "gate_output": true},
		{"command": "curl", "args": ["-H", "Authorization: Bearer argsecret123"], "gate_output": true},
		{"command": "run_script", "args": ["next-task"]},
		{"command": "uptime", "args": [], "gate_output": false}
	]`), 0644); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)

	w, err := Load(path)
	if err != nil {
		t.Fatalf("Load must not fail on a gate_output rule (a refusal to start takes the relay down): %v", err)
	}

	if w.Match("run_script", []string{"signal-read"}) {
		t.Error("a gate_output rule loaded as an auto-approve rule; its output would reach the agent ungated")
	}
	if w.Match("curl", []string{"-H", "Authorization: Bearer argsecret123"}) {
		t.Error("a gate_output command rule loaded as an auto-approve rule")
	}
	// Control: rules without the flag (absent or false) still load, so the
	// assertions above are not passing because Load dropped everything.
	if !w.Match("run_script", []string{"next-task"}) {
		t.Error("control: an ungated rule no longer loads")
	}
	if !w.Match("uptime", []string{}) {
		t.Error("control: a rule with gate_output false no longer loads")
	}
	if n := len(w.Rules()); n != 2 {
		t.Errorf("loaded %d rules, want 2", n)
	}

	out := logs.String()
	if got := strings.Count(out, "gate_output"); got != 2 {
		t.Errorf("want one gate_output skip line per skipped rule (2), got %d in log:\n%s", got, out)
	}
	if !strings.Contains(out, "signal-read") {
		t.Errorf("the skip line does not name the script:\n%s", out)
	}
	if strings.Contains(out, "argsecret123") {
		t.Errorf("the skip line echoed a command rule's arguments:\n%s", out)
	}
}
