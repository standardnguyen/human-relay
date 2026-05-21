// Package permissions provides a file-backed permission rule store and
// matcher for tool-call gating. Designed for use by the pi-relay-gate
// extension (and eventually other agents) which POSTs each tool call to the
// relay's /permission/check endpoint before allowing the call to proceed.
//
// The on-disk schema mirrors Claude Code's settings.local.json permissions
// block so rules port over with trivial massaging:
//
//	{
//	  "allow": ["Bash(ls:*)", "Read(/root/personal-wiki/**)"],
//	  "deny":  ["Bash(rm -rf /:*)", "Read(/root/.ssh/id_*)"],
//	  "ask":   []
//	}
//
// Lookup order on Check():
//  1. categorical hard-deny (secrets paths, destructive bash)
//  2. user deny list (first matching rule blocks)
//  3. user allow list (first matching rule proceeds)
//  4. user ask list (first matching rule routes to approval queue)
//  5. fallthrough = "ask" (fail-closed default for unknown calls)
package permissions

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

type Verdict string

const (
	VerdictAllow Verdict = "allow"
	VerdictDeny  Verdict = "deny"
	VerdictAsk   Verdict = "ask"
)

// Decision is what Check returns to the caller.
type Decision struct {
	Verdict Verdict `json:"verdict"`
	RuleID  string  `json:"rule_id,omitempty"` // raw rule string, "hard_deny:<reason>", or "default"
	Reason  string  `json:"reason,omitempty"`
}

// Config is the on-disk JSON shape.
type Config struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
	Ask   []string `json:"ask"`
}

// Rule is a parsed rule string.
type Rule struct {
	Tool    string // "Bash", "Read", "Write", "Edit", "*"
	Matcher string // Bash: command prefix; file tools: path glob
	Raw     string // original string for audit
}

type Permissions struct {
	mu    sync.RWMutex
	allow []Rule
	deny  []Rule
	ask   []Rule
	path  string
}

// Categorical hard-deny patterns. Independent of user rules; cannot be
// opened by misconfiguration. Keep this list short and obviously-safe.
var (
	hardDenyPathRe = regexp.MustCompile(`\.env$|credentials|\bid_rsa\b|\bid_ed25519\b|smbcredentials|/\.anthropic_key|/\.aws/|/\.gnupg/`)
	hardDenyBashRe = regexp.MustCompile(`\brm\s+-rf\s+/(\s|$)|:\(\)\{\s*:\|:&\s*\};:`)
)

// Load reads the rule file at path. A missing file is not an error — it
// returns an empty store that still applies the hard-deny list and falls
// through to "ask" on every call.
func Load(path string) (*Permissions, error) {
	p := &Permissions{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p, nil
		}
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.allow, err = parseRules(cfg.Allow); err != nil {
		return nil, fmt.Errorf("allow: %w", err)
	}
	if p.deny, err = parseRules(cfg.Deny); err != nil {
		return nil, fmt.Errorf("deny: %w", err)
	}
	if p.ask, err = parseRules(cfg.Ask); err != nil {
		return nil, fmt.Errorf("ask: %w", err)
	}
	return p, nil
}

// Save serialises the current ruleset back to disk.
func (p *Permissions) Save() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cfg := Config{
		Allow: rulesRaw(p.allow),
		Deny:  rulesRaw(p.deny),
		Ask:   rulesRaw(p.ask),
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.path, data, 0644)
}

// Check returns the verdict for a tool call. input is the tool's argument
// map (e.g. {"command": "ls -la"} for Bash, {"file_path": "/x"} for Read).
func (p *Permissions) Check(tool string, input map[string]any) Decision {
	if hard := hardDeny(tool, input); hard != "" {
		return Decision{Verdict: VerdictDeny, RuleID: "hard_deny", Reason: hard}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, r := range p.deny {
		if matchRule(r, tool, input) {
			return Decision{Verdict: VerdictDeny, RuleID: r.Raw, Reason: "denied by rule"}
		}
	}
	for _, r := range p.allow {
		if matchRule(r, tool, input) {
			return Decision{Verdict: VerdictAllow, RuleID: r.Raw}
		}
	}
	for _, r := range p.ask {
		if matchRule(r, tool, input) {
			return Decision{Verdict: VerdictAsk, RuleID: r.Raw}
		}
	}
	return Decision{Verdict: VerdictAsk, RuleID: "default", Reason: "no matching rule (fail-closed default)"}
}

// Rules returns a snapshot of the current rule set, for audit/inspection.
func (p *Permissions) Rules() Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return Config{
		Allow: rulesRaw(p.allow),
		Deny:  rulesRaw(p.deny),
		Ask:   rulesRaw(p.ask),
	}
}

var ruleRe = regexp.MustCompile(`^([A-Za-z_*][A-Za-z0-9_]*)\((.+)\)$`)

func parseRules(raw []string) ([]Rule, error) {
	out := make([]Rule, 0, len(raw))
	for _, s := range raw {
		m := ruleRe.FindStringSubmatch(s)
		if m == nil {
			return nil, fmt.Errorf("unparseable rule: %q", s)
		}
		out = append(out, Rule{Tool: m[1], Matcher: m[2], Raw: s})
	}
	return out, nil
}

func rulesRaw(rs []Rule) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Raw
	}
	return out
}

func matchRule(r Rule, tool string, input map[string]any) bool {
	if r.Tool != "*" && !strings.EqualFold(r.Tool, tool) {
		return false
	}
	switch strings.ToLower(tool) {
	case "bash":
		cmd, _ := input["command"].(string)
		prefix := strings.TrimSuffix(r.Matcher, ":*")
		return cmd == prefix || strings.HasPrefix(cmd, prefix+" ")
	case "read", "write", "edit":
		path := stringFromAny(input, "file_path", "path")
		return globMatch(r.Matcher, path)
	default:
		// Unknown tool: only "*" rules can match it. Even then we need a
		// matcher; treat ":*" as universal, anything else as no-match.
		return r.Matcher == ":*" || r.Matcher == "*"
	}
}

func stringFromAny(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// globMatch implements minimal glob: `**` matches any substring including
// `/`; `*` matches any substring not containing `/`. Anchored.
func globMatch(glob, s string) bool {
	if glob == "" || s == "" {
		return false
	}
	const dbl = "\x00DBL\x00"
	escaped := regexp.QuoteMeta(glob)
	// QuoteMeta escaped our `*`s. Undo for our two wildcards.
	escaped = strings.ReplaceAll(escaped, `\*\*`, dbl)
	escaped = strings.ReplaceAll(escaped, `\*`, `[^/]*`)
	escaped = strings.ReplaceAll(escaped, dbl, `.*`)
	re, err := regexp.Compile(`^` + escaped + `$`)
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

func hardDeny(tool string, input map[string]any) string {
	switch strings.ToLower(tool) {
	case "bash":
		cmd, _ := input["command"].(string)
		if hardDenyBashRe.MatchString(cmd) {
			return "categorical deny: dangerous command pattern"
		}
	case "read", "write", "edit":
		path := stringFromAny(input, "file_path", "path")
		if hardDenyPathRe.MatchString(path) {
			return fmt.Sprintf("categorical deny: secrets path (%s)", path)
		}
	}
	return ""
}
