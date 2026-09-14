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
	req := s.Add("echo", []string{"hi"}, "atomic approve test", "", false, 5, "")

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
	req := s.Add("echo", []string{"hi"}, "gated approve test", "", false, 5, "")

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

	denied := s.Add("echo", []string{"hi"}, "denied", "", false, 5, "")
	s.Deny(denied.ID, "nope")
	if ok, r := s.Approve(denied.ID, false); ok || r != nil {
		t.Errorf("Approve on denied request = (%v, %v), want (false, nil)", ok, r)
	}
	if got := s.Get(denied.ID); got.Status != StatusDenied {
		t.Errorf("denied request status = %s, want %s", got.Status, StatusDenied)
	}

	withdrawn := s.Add("echo", []string{"hi"}, "withdrawn", "", false, 5, "")
	s.Withdraw(withdrawn.ID, "agent gave up")
	if ok, r := s.Approve(withdrawn.ID, false); ok || r != nil {
		t.Errorf("Approve on withdrawn request = (%v, %v), want (false, nil)", ok, r)
	}
}

// TestApproveReturnsCopy guards the same copy-semantics Get has: a caller must
// not be handed a live pointer it could mutate under the store's lock.
func TestApproveReturnsCopy(t *testing.T) {
	s := New()
	req := s.Add("echo", []string{"hi"}, "copy test", "", false, 5, "")

	ok, approved := s.Approve(req.ID, false)
	if !ok {
		t.Fatal("Approve returned ok=false on a pending request")
	}
	approved.Command = "mutated"
	if got := s.Get(req.ID); got.Command != "echo" {
		t.Errorf("stored command = %q, want %q -- Approve handed out a live pointer", got.Command, "echo")
	}
}

// TestAddScriptTypedRaceWithReaders pins finding #26 at the store layer: the
// script-family Type is set inside the struct literal, before the request is
// published into the map, so a concurrent List/Get never races the write.
// The test is concurrent on purpose — if a future change goes back to setting
// Type (or any other field) on the returned pointer after Add* returns, the
// reader goroutines here give `go test -race` an unsynchronized read to pair
// it with.
func TestAddScriptTypedRaceWithReaders(t *testing.T) {
	s := New()

	const writes = 50
	done := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				for _, r := range s.List("") {
					_ = r.Type
					_ = r.StdinSHA256
					if got := s.Get(r.ID); got != nil {
						_ = got.Type
						_ = got.StdinSHA256
					}
				}
			}
		}()
	}

	var writers sync.WaitGroup
	ids := make([]string, writes)
	for i := 0; i < writes; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			r := s.AddScriptTyped("script_create", "racy", nil, "finding 26 store race test", 0, []byte("#!/bin/sh\necho racy\n"), "")
			ids[i] = r.ID
		}(i)
	}
	writers.Wait()
	close(done)
	readers.Wait()

	for i, id := range ids {
		r := s.Get(id)
		if r == nil {
			t.Fatalf("write %d: request %s missing", i, id)
		}
		if r.Type != "script_create" {
			t.Errorf("write %d: Type = %q, want %q", i, r.Type, "script_create")
		}
	}
}

// TestAddScriptTypedPublishesStdinAtConstruction pins the invariant finding #8
// leans on: the script body — and its digest, which the whitelist keys script
// creates on — must already be set by the time AddScriptTyped returns, because
// the request is published (and the whitelist matcher woken) inside that call.
// A caller filling stdin in afterwards would race the matcher, which would then
// key the rule off an empty body.
func TestAddScriptTypedPublishesStdinAtConstruction(t *testing.T) {
	s := New()
	body := []byte("#!/bin/sh\necho hi\n")

	r := s.AddScriptTyped("script_create", "constructed", nil, "stdin at construction", 0, body, "")
	if r.StdinSHA256 != StdinDigest(body) {
		t.Errorf("returned request digest = %q, want %q", r.StdinSHA256, StdinDigest(body))
	}
	if r.StdinLen != len(body) {
		t.Errorf("returned request stdin_len = %d, want %d", r.StdinLen, len(body))
	}

	got := s.Get(r.ID)
	if got == nil {
		t.Fatal("request missing from store")
	}
	if string(got.Stdin) != string(body) {
		t.Errorf("stored stdin = %q, want %q", got.Stdin, body)
	}
	if got.StdinSHA256 != StdinDigest(body) {
		t.Errorf("stored digest = %q, want %q", got.StdinSHA256, StdinDigest(body))
	}
}

// TestStdinDigestDistinguishesBodies is the property the content-keyed
// whitelist rests on: same bytes, same key; different bytes, different key.
func TestStdinDigestDistinguishesBodies(t *testing.T) {
	a := []byte("#!/bin/sh\necho reviewed\n")
	b := []byte("#!/bin/sh\necho pwned\n")

	if StdinDigest(a) != StdinDigest(a) {
		t.Error("digest is not stable for identical input")
	}
	if StdinDigest(a) == StdinDigest(b) {
		t.Error("digest collides across different bodies")
	}
	if len(StdinDigest(a)) != 64 {
		t.Errorf("digest length = %d, want 64 hex chars", len(StdinDigest(a)))
	}
	if StdinDigest(nil) != "" {
		t.Errorf("empty input digest = %q, want empty string", StdinDigest(nil))
	}
}

// TestAddScriptDefaultsToScriptType keeps the plain run_script path pinned to
// the untyped Type after the AddScriptTyped refactor.
func TestAddScriptDefaultsToScriptType(t *testing.T) {
	s := New()
	r := s.AddScript("some-script", []string{"a"}, "type default test", 0, "")
	got := s.Get(r.ID)
	if got == nil {
		t.Fatal("request missing from store")
	}
	if got.Type != "script" {
		t.Errorf("Type = %q, want %q", got.Type, "script")
	}
}

// TestAddRecordsClient pins the attribution contract: the client is a
// construction argument, so it is set before the request is published into the
// store's map — never filled in afterwards (the finding #26 race).
func TestAddRecordsClient(t *testing.T) {
	s := New()
	req := s.Add("echo", []string{"hi"}, "attribution", "", false, 5, "cc-115")
	if req.Client != "cc-115" {
		t.Errorf("returned request Client = %q, want cc-115", req.Client)
	}
	if got := s.Get(req.ID); got.Client != "cc-115" {
		t.Errorf("stored request Client = %q, want cc-115", got.Client)
	}
}
