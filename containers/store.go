package containers

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

type Container struct {
	CTID        int       `json:"ctid"`
	IP          string    `json:"ip"`
	Hostname    string    `json:"hostname"`
	HasRelaySSH bool      `json:"has_relay_ssh"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Store struct {
	path       string
	mu         sync.RWMutex
	containers map[int]*Container
}

func NewStore(path string) (*Store, error) {
	s := &Store{
		path:       path,
		containers: make(map[int]*Container),
	}

	data, err := os.ReadFile(path)
	if err == nil {
		var list []*Container
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, c := range list {
			s.containers[c.CTID] = c
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

func (s *Store) sorted() []*Container {
	list := make([]*Container, 0, len(s.containers))
	for _, c := range s.containers {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].CTID < list[j].CTID
	})
	return list
}

// Register upserts a container record.
func (s *Store) Register(ctid int, ip, hostname string, hasRelaySSH bool) (*Container, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	existing := s.containers[ctid]

	c := &Container{
		CTID:        ctid,
		IP:          ip,
		Hostname:    hostname,
		HasRelaySSH: hasRelaySSH,
		UpdatedAt:   now,
	}
	if existing != nil {
		c.CreatedAt = existing.CreatedAt
	} else {
		c.CreatedAt = now
	}

	s.containers[ctid] = c
	if err := s.save(); err != nil {
		return nil, fmt.Errorf("upsert container %d: %w", ctid, err)
	}
	return c, nil
}

// Get retrieves a single container by CTID. Returns nil if not found.
func (s *Store) Get(ctid int) (*Container, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.containers[ctid], nil
}

// List returns all registered containers ordered by CTID.
func (s *Store) List() ([]*Container, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.sorted()
	if len(list) == 0 {
		return nil, nil
	}
	return list, nil
}

// Delete removes a container from the registry.
func (s *Store) Delete(ctid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.containers[ctid]; !ok {
		return fmt.Errorf("container %d not found", ctid)
	}
	delete(s.containers, ctid)
	return s.save()
}

func (s *Store) Close() error {
	return nil
}
