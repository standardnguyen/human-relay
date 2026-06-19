package machines

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machines.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, path
}

func TestRegisterAndGet(t *testing.T) {
	s, _ := newTestStore(t)

	m, err := s.Register("corsair-win", "100.106.181.59", "esthie", ShellPowerShell, "")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if m.Name != "corsair-win" || m.Host != "100.106.181.59" || m.SSHUser != "esthie" || m.Shell != ShellPowerShell {
		t.Fatalf("unexpected machine: %+v", m)
	}

	got, _ := s.Get("corsair-win")
	if got == nil || got.SSHUser != "esthie" {
		t.Fatalf("Get returned %+v", got)
	}
}

func TestShellDefaultsToPosix(t *testing.T) {
	s, _ := newTestStore(t)
	m, _ := s.Register("box", "10.0.0.5", "ops", "", "")
	if m.Shell != ShellPosix {
		t.Fatalf("expected default shell posix, got %q", m.Shell)
	}
}

func TestUpsertPreservesFields(t *testing.T) {
	s, _ := newTestStore(t)
	first, _ := s.Register("box", "10.0.0.5", "ops", ShellPowerShell, "/root/.ssh-data/id_rsa")

	// Re-register with only host changed; empty fields must preserve.
	second, _ := s.Register("box", "10.0.0.9", "", "", "")
	if second.Host != "10.0.0.9" {
		t.Fatalf("host not updated: %q", second.Host)
	}
	if second.SSHUser != "ops" {
		t.Fatalf("ssh_user not preserved: %q", second.SSHUser)
	}
	if second.Shell != ShellPowerShell {
		t.Fatalf("shell not preserved: %q", second.Shell)
	}
	if second.IdentityFile != "/root/.ssh-data/id_rsa" {
		t.Fatalf("identity_file not preserved: %q", second.IdentityFile)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed on upsert: %v -> %v", first.CreatedAt, second.CreatedAt)
	}
}

func TestListSortedAndDelete(t *testing.T) {
	s, _ := newTestStore(t)
	s.Register("zebra", "10.0.0.2", "a", "", "")
	s.Register("alpha", "10.0.0.1", "b", "", "")

	list, _ := s.List()
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "zebra" {
		t.Fatalf("expected sorted [alpha zebra], got %+v", list)
	}

	if err := s.Delete("alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := s.Get("alpha"); got != nil {
		t.Fatal("alpha still present after delete")
	}
	if err := s.Delete("nope"); err == nil {
		t.Fatal("expected error deleting nonexistent machine")
	}
}

func TestPersistenceReload(t *testing.T) {
	s, path := newTestStore(t)
	s.Register("corsair-win", "100.106.181.59", "esthie", ShellPowerShell, "")

	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload NewStore: %v", err)
	}
	got, _ := reloaded.Get("corsair-win")
	if got == nil || got.Shell != ShellPowerShell || got.SSHUser != "esthie" {
		t.Fatalf("reloaded machine wrong: %+v", got)
	}
}
