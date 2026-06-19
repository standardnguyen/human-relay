// Package machines is a registry of non-LXC SSH targets — Windows
// workstations, bare-metal hosts, VMs, WSL instances — keyed by a string name
// instead of a CTID. It is the first-class home for everything that used to be
// shoehorned into the container registry as a "pseudo-CTID". Containers stay in
// the containers package (keyed by int CTID); anything that isn't an LXC
// container lives here.
package machines

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// Shell identifies how the remote command line is constructed for a machine.
const (
	ShellPosix      = "posix"      // sh/bash; cat > file; sh -c
	ShellPowerShell = "powershell" // Windows; -EncodedCommand; [IO.File]::WriteAllBytes
)

// Machine is a single SSH-reachable, non-LXC target.
type Machine struct {
	Name         string    `json:"name"`                    // primary key, e.g. "corsair-win"
	Host         string    `json:"host"`                    // IP or hostname the relay SSHes to
	SSHUser      string    `json:"ssh_user"`                // login user (no root default — arbitrary machines vary)
	Shell        string    `json:"shell"`                   // ShellPosix (default) or ShellPowerShell
	IdentityFile string    `json:"identity_file,omitempty"` // optional key override (else relay default)
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Store struct {
	path     string
	mu       sync.RWMutex
	machines map[string]*Machine
}

func NewStore(path string) (*Store, error) {
	s := &Store{
		path:     path,
		machines: make(map[string]*Machine),
	}

	data, err := os.ReadFile(path)
	if err == nil {
		var list []*Machine
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, m := range list {
			s.machines[m.Name] = m
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return s, nil
}

func (s *Store) save() error {
	list := s.sorted()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}

func (s *Store) sorted() []*Machine {
	list := make([]*Machine, 0, len(s.machines))
	for _, m := range s.machines {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// Register upserts a machine record. On update, an empty shell/ssh_user/
// identity_file preserves the existing value (so a partial re-register doesn't
// blank fields), matching the containers registry's upsert semantics. A blank
// shell defaults to posix.
func (s *Store) Register(name, host, sshUser, shell, identityFile string) (*Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	existing := s.machines[name]

	m := &Machine{
		Name:         name,
		Host:         host,
		SSHUser:      sshUser,
		Shell:        shell,
		IdentityFile: identityFile,
		UpdatedAt:    now,
	}
	if existing != nil {
		m.CreatedAt = existing.CreatedAt
		if m.SSHUser == "" {
			m.SSHUser = existing.SSHUser
		}
		if m.Shell == "" {
			m.Shell = existing.Shell
		}
		if m.IdentityFile == "" {
			m.IdentityFile = existing.IdentityFile
		}
	} else {
		m.CreatedAt = now
	}
	if m.Shell == "" {
		m.Shell = ShellPosix
	}

	s.machines[name] = m
	if err := s.save(); err != nil {
		return nil, fmt.Errorf("upsert machine %q: %w", name, err)
	}
	return m, nil
}

// Get retrieves a single machine by name. Returns nil if not found.
func (s *Store) Get(name string) (*Machine, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.machines[name], nil
}

// List returns all registered machines ordered by name.
func (s *Store) List() ([]*Machine, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.sorted()
	if len(list) == 0 {
		return nil, nil
	}
	return list, nil
}

// Delete removes a machine from the registry.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.machines[name]; !ok {
		return fmt.Errorf("machine %q not found", name)
	}
	delete(s.machines, name)
	return s.save()
}

func (s *Store) Close() error {
	return nil
}
