package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	p, err := Load("/nonexistent/path.json")
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	d := p.Check("Bash", map[string]any{"command": "ls"})
	if d.Verdict != VerdictAsk || d.RuleID != "default" {
		t.Fatalf("expected default ask, got %+v", d)
	}
}

func TestLoadMalformedRule(t *testing.T) {
	path := writeConfig(t, `{"allow":["totally not a rule"]}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error for malformed rule")
	}
}

func TestCheckAllowBashPrefix(t *testing.T) {
	path := writeConfig(t, `{"allow":["Bash(ls:*)","Bash(git status:*)"]}`)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cmd  string
		want Verdict
	}{
		{"ls", VerdictAllow},
		{"ls -la", VerdictAllow},
		{"git status", VerdictAllow},
		{"git status --short", VerdictAllow},
		{"git stash", VerdictAsk},
		{"lsblk", VerdictAsk}, // prefix must be followed by space, not extra chars
	}
	for _, c := range cases {
		d := p.Check("Bash", map[string]any{"command": c.cmd})
		if d.Verdict != c.want {
			t.Errorf("cmd=%q: want %s, got %s (rule=%s)", c.cmd, c.want, d.Verdict, d.RuleID)
		}
	}
}

func TestCheckDenyBeforeAllow(t *testing.T) {
	path := writeConfig(t, `{
		"allow": ["Bash(rm:*)"],
		"deny":  ["Bash(rm -rf:*)"]
	}`)
	p, _ := Load(path)
	d := p.Check("Bash", map[string]any{"command": "rm -rf /home/foo"})
	if d.Verdict != VerdictDeny {
		t.Fatalf("deny should beat allow: %+v", d)
	}
}

func TestHardDenyOverridesAllow(t *testing.T) {
	path := writeConfig(t, `{"allow":["Read(/root/.ssh/id_ed25519)"]}`)
	p, _ := Load(path)
	d := p.Check("Read", map[string]any{"file_path": "/root/.ssh/id_ed25519"})
	if d.Verdict != VerdictDeny || d.RuleID != "hard_deny" {
		t.Fatalf("hard-deny must override allow: %+v", d)
	}
}

func TestHardDenyBashRmRf(t *testing.T) {
	p, _ := Load("/nonexistent")
	d := p.Check("Bash", map[string]any{"command": "rm -rf /"})
	if d.Verdict != VerdictDeny || d.RuleID != "hard_deny" {
		t.Fatalf("hard-deny rm -rf /: %+v", d)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"/root/personal-wiki/**", "/root/personal-wiki/content/x.md", true},
		{"/root/personal-wiki/**", "/root/personal-wiki/x.md", true},
		{"/root/personal-wiki/**", "/etc/passwd", false},
		{"/tmp/*.log", "/tmp/foo.log", true},
		{"/tmp/*.log", "/tmp/sub/foo.log", false},
		{"/root/.ssh/id_*", "/root/.ssh/id_ed25519", true},
	}
	for _, c := range cases {
		if got := globMatch(c.glob, c.path); got != c.want {
			t.Errorf("glob %q vs %q: want %v, got %v", c.glob, c.path, c.want, got)
		}
	}
}

func TestCheckAskExplicit(t *testing.T) {
	path := writeConfig(t, `{"ask":["Bash(git push:*)"]}`)
	p, _ := Load(path)
	d := p.Check("Bash", map[string]any{"command": "git push origin dev"})
	if d.Verdict != VerdictAsk || d.RuleID == "default" {
		t.Fatalf("expected explicit ask, got %+v", d)
	}
}

func TestCheckDefaultFailClosed(t *testing.T) {
	p, _ := Load("/nonexistent")
	d := p.Check("Write", map[string]any{"file_path": "/tmp/x"})
	if d.Verdict != VerdictAsk || d.RuleID != "default" {
		t.Fatalf("expected default ask, got %+v", d)
	}
}

func TestSaveRoundtrip(t *testing.T) {
	path := writeConfig(t, `{"allow":["Bash(ls:*)"],"deny":["Bash(rm:*)"]}`)
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	p2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rs := p2.Rules()
	if len(rs.Allow) != 1 || rs.Allow[0] != "Bash(ls:*)" {
		t.Fatalf("roundtrip allow lost: %+v", rs)
	}
	if len(rs.Deny) != 1 || rs.Deny[0] != "Bash(rm:*)" {
		t.Fatalf("roundtrip deny lost: %+v", rs)
	}
}
