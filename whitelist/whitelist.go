package whitelist

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

type Rule struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// storedRule is a rule as read from disk. gate_output ("auto-approve, but
// withhold the output") was removed; it is still decoded so that Load can
// refuse such a rule instead of silently loading it as a plain auto-approve
// rule, which would show the agent output that was meant to stay gated.
type storedRule struct {
	Rule
	GateOutput bool `json:"gate_output"`
}

// scriptTools key their rules on a script name, which is safe to log. Every
// other rule's args are a command line or URL and may carry a secret.
var scriptTools = map[string]bool{"run_script": true, "create_script": true, "create_then_run": true}

type Whitelist struct {
	mu    sync.RWMutex
	rules []Rule
	path  string
}

func Load(path string) (*Whitelist, error) {
	w := &Whitelist{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return w, nil
		}
		return nil, err
	}
	var stored []storedRule
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	for _, r := range stored {
		if r.GateOutput {
			name := r.Command
			if scriptTools[r.Command] && len(r.Args) > 0 {
				name += " " + r.Args[0]
			}
			log.Printf("WHITELIST: SKIPPING rule %q from %s: it sets gate_output, which was removed; matching requests now wait for manual approval (use Approve (Gated)), and the rule is dropped from the file at the next whitelist change", name, path)
			continue
		}
		w.rules = append(w.rules, r.Rule)
	}
	return w, nil
}

func (w *Whitelist) Match(command string, args []string) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for _, r := range w.rules {
		if r.Command == command && argsEqual(r.Args, args) {
			return true
		}
	}
	return false
}

func (w *Whitelist) Rules() []Rule {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]Rule, len(w.rules))
	copy(out, w.rules)
	return out
}

func (w *Whitelist) Add(command string, args []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Re-adding an existing rule is a no-op instead of a duplicate
	for _, r := range w.rules {
		if r.Command == command && argsEqual(r.Args, args) {
			return
		}
	}
	w.rules = append(w.rules, Rule{Command: command, Args: args})
}

func (w *Whitelist) Remove(command string, args []string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, r := range w.rules {
		if r.Command == command && argsEqual(r.Args, args) {
			w.rules = append(w.rules[:i], w.rules[i+1:]...)
			return true
		}
	}
	return false
}

func (w *Whitelist) Save() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	data, err := json.MarshalIndent(w.rules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(w.path, data, 0644)
}

func argsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
