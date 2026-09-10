package store

import (
	"sync"
	"testing"
)

// TestApproveIsAtomicUnderContention pins finding #52 at the store layer: many
// callers approving the same pending request concurrently must produce exactly
// one ok=true, because each ok=true spawns an execution in the web handler.
func TestApproveIsAtomicUnderContention(t *testing.T) {
	s := New()
	req := s.Add("echo", []string{"hi"}, "atomic approve test", "", false, 5)

	const callers = 32
	start := make(chan struct{})
	results := make([]bool, callers)
	copies := make([]*Request, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], copies[i] = s.Approve(req.ID, false)
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for i, ok := range results {
		if ok {
			won++
			if copies[i] == nil {
				t.Errorf("caller %d: ok=true but nil request", i)
				continue
			}
			if copies[i].Status != StatusApproved {
				t.Errorf("caller %d: returned status %s, want %s", i, copies[i].Status, StatusApproved)
			}
			if copies[i].DecidedAt == nil {
				t.Errorf("caller %d: DecidedAt not stamped", i)
			}
		} else if copies[i] != nil {
			t.Errorf("caller %d: ok=false must return a nil request, got %+v", i, copies[i])
		}
	}
	if won != 1 {
		t.Fatalf("expected exactly 1 successful Approve, got %d", won)
	}

	if got := s.Get(req.ID); got.Status != StatusApproved {
		t.Errorf("stored status = %s, want %s", got.Status, StatusApproved)
	}
}

func TestApproveGatesOutput(t *testing.T) {
	s := New()
	req := s.Add("echo", []string{"hi"}, "gated approve test", "", false, 5)

	ok, approved := s.Approve(req.ID, true)
	if !ok {
		t.Fatal("Approve returned ok=false on a pending request")
	}
	if !approved.OutputGated {
		t.Error("returned request should be output-gated")
	}
	if got := s.Get(req.ID); !got.OutputGated {
		t.Error("stored request should be output-gated")
	}
}

func TestApproveRejectsNonPending(t *testing.T) {
	s := New()

	if ok, r := s.Approve("no-such-id", false); ok || r != nil {
		t.Errorf("Approve on unknown id = (%v, %v), want (false, nil)", ok, r)
	}

	denied := s.Add("echo", []string{"hi"}, "denied", "", false, 5)
	s.Deny(denied.ID, "nope")
	if ok, r := s.Approve(denied.ID, false); ok || r != nil {
		t.Errorf("Approve on denied request = (%v, %v), want (false, nil)", ok, r)
	}
	if got := s.Get(denied.ID); got.Status != StatusDenied {
		t.Errorf("denied request status = %s, want %s", got.Status, StatusDenied)
	}

	withdrawn := s.Add("echo", []string{"hi"}, "withdrawn", "", false, 5)
	s.Withdraw(withdrawn.ID, "agent gave up")
	if ok, r := s.Approve(withdrawn.ID, false); ok || r != nil {
		t.Errorf("Approve on withdrawn request = (%v, %v), want (false, nil)", ok, r)
	}
}

// TestApproveReturnsCopy guards the same copy-semantics Get has: a caller must
// not be handed a live pointer it could mutate under the store's lock.
func TestApproveReturnsCopy(t *testing.T) {
	s := New()
	req := s.Add("echo", []string{"hi"}, "copy test", "", false, 5)

	ok, approved := s.Approve(req.ID, false)
	if !ok {
		t.Fatal("Approve returned ok=false on a pending request")
	}
	approved.Command = "mutated"
	if got := s.Get(req.ID); got.Command != "echo" {
		t.Errorf("stored command = %q, want %q -- Approve handed out a live pointer", got.Command, "echo")
	}
}
