package containers

import (
	"os"
	"path/filepath"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "containers.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRegisterAndGet(t *testing.T) {
	s := tempStore(t)

	c, err := s.Register(133, "192.168.10.90", "archivebox", true)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if c.CTID != 133 || c.IP != "192.168.10.90" || c.Hostname != "archivebox" || !c.HasRelaySSH {
		t.Fatalf("unexpected container: %+v", c)
	}

	got, err := s.Get(133)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CTID != 133 || got.IP != "192.168.10.90" {
		t.Fatalf("Get returned wrong data: %+v", got)
	}
}

func TestGetNotFound(t *testing.T) {
	s := tempStore(t)

	got, err := s.Get(999)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing container, got %+v", got)
	}
}

func TestRegisterUpsert(t *testing.T) {
	s := tempStore(t)

	s.Register(133, "192.168.10.90", "archivebox", false)

	// Update the same CTID
	c, err := s.Register(133, "192.168.10.91", "archivebox-v2", true)
	if err != nil {
		t.Fatalf("Register upsert: %v", err)
	}
	if c.IP != "192.168.10.91" || c.Hostname != "archivebox-v2" || !c.HasRelaySSH {
		t.Fatalf("upsert didn't update: %+v", c)
	}

	// Should still be only 1 container
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 container after upsert, got %d", len(list))
	}
}

func TestList(t *testing.T) {
	s := tempStore(t)

	s.Register(100, "192.168.10.52", "ingress", false)
	s.Register(133, "192.168.10.90", "archivebox", true)
	s.Register(115, "192.168.10.66", "claude-code", false)

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 containers, got %d", len(list))
	}

	// Should be ordered by CTID
	if list[0].CTID != 100 || list[1].CTID != 115 || list[2].CTID != 133 {
		t.Fatalf("wrong order: %d, %d, %d", list[0].CTID, list[1].CTID, list[2].CTID)
	}
}

func TestListEmpty(t *testing.T) {
	s := tempStore(t)

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list != nil {
		t.Fatalf("expected nil for empty list, got %d items", len(list))
	}
}

func TestDelete(t *testing.T) {
	s := tempStore(t)

	s.Register(133, "192.168.10.90", "archivebox", true)

	if err := s.Delete(133); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := s.Get(133)
	if got != nil {
		t.Fatalf("container still exists after delete: %+v", got)
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := tempStore(t)

	err := s.Delete(999)
	if err == nil {
		t.Fatal("expected error deleting nonexistent container")
	}
}

func TestNewStoreCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "containers.json")

	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// File shouldn't exist yet (no data written)
	s.Register(100, "192.168.10.52", "ingress", false)
	s.Close()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("JSON file was not created after Register")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "containers.json")

	// Write some data
	s1, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s1.Register(133, "192.168.10.90", "archivebox", true)
	s1.Close()

	// Reopen and verify
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.Get(133)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got == nil || got.IP != "192.168.10.90" || got.Hostname != "archivebox" || !got.HasRelaySSH {
		t.Fatalf("data not persisted: %+v", got)
	}
}
