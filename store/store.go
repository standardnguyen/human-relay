package store

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusDenied   Status = "denied"
	StatusRunning  Status = "running"
	StatusComplete Status = "complete"
	StatusTimeout  Status = "timeout"
	StatusError    Status = "error"
)

type Request struct {
	ID         string    `json:"id"`
	Command    string    `json:"command"`
	Args       []string  `json:"args"`
	Reason     string    `json:"reason"`
	WorkingDir string    `json:"working_dir,omitempty"`
	Shell      bool      `json:"shell"`
	Timeout    int       `json:"timeout"`
	Status     Status    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
	DenyReason string    `json:"deny_reason,omitempty"`
	Result     *Result   `json:"result,omitempty"`
}

type Result struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type Store struct {
	mu       sync.RWMutex
	requests map[string]*Request
	order    []string // insertion order
	notify   chan string
}

func New() *Store {
	return &Store{
		requests: make(map[string]*Request),
		notify:   make(chan string, 100),
	}
}

func (s *Store) Add(cmd string, args []string, reason, workingDir string, shell bool, timeout int) *Request {
	id := generateID()
	r := &Request{
		ID:         id,
		Command:    cmd,
		Args:       args,
		Reason:     reason,
		WorkingDir: workingDir,
		Shell:      shell,
		Timeout:    timeout,
		Status:     StatusPending,
		CreatedAt:  time.Now(),
	}
	s.mu.Lock()
	s.requests[id] = r
	s.order = append(s.order, id)
	s.mu.Unlock()

	// Non-blocking send to notify listeners
	select {
	case s.notify <- id:
	default:
	}

	return r
}

func (s *Store) Get(id string) *Request {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.requests[id]
	if !ok {
		return nil
	}
	// Return a copy
	cp := *r
	if r.Result != nil {
		rc := *r.Result
		cp.Result = &rc
	}
	return &cp
}

func (s *Store) SetStatus(id string, status Status) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false
	}
	r.Status = status
	now := time.Now()
	if status == StatusApproved || status == StatusDenied {
		r.DecidedAt = &now
	}
	return true
}

func (s *Store) Deny(id string, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false
	}
	r.Status = StatusDenied
	r.DenyReason = reason
	now := time.Now()
	r.DecidedAt = &now
	return true
}

func (s *Store) SetResult(id string, result *Result, status Status) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false
	}
	r.Result = result
	r.Status = status
	return true
}

func (s *Store) List(filter Status) []*Request {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Request
	for _, id := range s.order {
		r := s.requests[id]
		if filter == "" || r.Status == filter {
			cp := *r
			if r.Result != nil {
				rc := *r.Result
				cp.Result = &rc
			}
			out = append(out, &cp)
		}
	}
	return out
}

// Subscribe returns a channel that receives request IDs when new requests are added.
func (s *Store) Subscribe() <-chan string {
	return s.notify
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
