package whitelist

import (
	"encoding/json"
	"os"
	"sync"
)

type Rule struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

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
	if err := json.Unmarshal(data, &w.rules); err != nil {
		return nil, err
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
	// Don't add duplicates
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
