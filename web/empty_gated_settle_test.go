package web

// executeRequest used to publish a finished request in two store updates:
// SetResult marked it complete with output_gated still set, and only after a
// log line and an audit write did it auto-release an empty result. A
// get_result poll, or a submit-and-wait `wait`, landing between the two read
// status complete, output_gated true and a 0-byte placeholder, so an agent
// following the output-gating rule stopped and waited for a Release click
// that was never needed.
//
// A tight poll loop only catches that window now and then. These tests watch
// it deterministically: executeRequest's log lines are written synchronously
// from the goroutine running it, so a log writer that snapshots the request
// on every line sees the store exactly as a poller could at each of those
// points, including the line printed between the two updates.

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/standardnguyen/human-relay/audit"
	"github.com/standardnguyen/human-relay/executor"
	"github.com/standardnguyen/human-relay/store"
)

// published is the request as the store held it when one log line was written.
type published struct {
	line        string
	status      store.Status
	gated       bool
	emptyResult bool // a result is stored and it has no stdout and no stderr
}

func (p published) settled() bool {
	return p.status == store.StatusComplete || p.status == store.StatusError
}

// snapshotWriter is a log output that records the request's stored state on
// every write.
type snapshotWriter struct {
	s  *store.Store
	id string

	mu   sync.Mutex
	seen []published
}

func (w *snapshotWriter) Write(p []byte) (int, error) {
	if r := w.s.Get(w.id); r != nil {
		empty := r.Result != nil && r.Result.Stdout == "" && r.Result.Stderr == ""
		w.mu.Lock()
		w.seen = append(w.seen, published{strings.TrimSpace(string(p)), r.Status, r.OutputGated, empty})
		w.mu.Unlock()
	}
	return len(p), nil
}

// runGated approves command as "approve (gated)" and runs it through
// executeRequest, returning the store, the request id, every state the store
// held at each log line, and the audit log it wrote.
func runGated(t *testing.T, command string, args ...string) (*store.Store, string, []published, string) {
	t.Helper()
	s := store.New()
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	al, err := audit.NewLogger(auditPath)
	if err != nil {
		t.Fatalf("audit logger: %v", err)
	}
	t.Cleanup(func() { al.Close() })
	h := NewHandler(s, executor.New(executor.Config{DefaultTimeout: 10, MaxTimeout: 10}), al)

	req := s.Add(command, args, "empty gated settle test", "", false, 10, "test-client")
	ok, approved := s.Approve(req.ID, true)
	if !ok {
		t.Fatal("gated approve failed")
	}

	w := &snapshotWriter{s: s, id: req.ID}
	prev := log.Writer()
	log.SetOutput(w)
	// Deferred so a panic in executeRequest cannot leave the process-global
	// logger pointed at this writer for every later test in the package.
	defer log.SetOutput(prev)
	h.executeRequest(approved)

	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return s, req.ID, append([]published(nil), w.seen...), string(raw)
}

func TestExecuteRequestNeverPublishesEmptyResultGated(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    store.Status
	}{
		{"exit 0, no output", "true", store.StatusComplete},
		{"exit 1, no output", "false", store.StatusError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, id, seen, auditLog := runGated(t, c.command)

			sawSettled := false
			for _, p := range seen {
				if !p.settled() {
					continue
				}
				sawSettled = true
				if p.gated && p.emptyResult {
					t.Errorf("request %s was published as status=%s, output_gated=true with an empty result (at log line %q): "+
						"a get_result or wait landing here reads a 0-byte gated placeholder and waits for a Release click that is never needed",
						id, p.status, p.line)
				}
			}
			// A seam that never fires after the result is stored passes
			// without testing anything.
			if !sawSettled {
				t.Fatalf("no log line was written after the result was stored (saw %+v); the seam no longer covers the settled window", seen)
			}

			final := s.Get(id)
			if final.Status != c.want || final.OutputGated {
				t.Errorf("final state status=%s output_gated=%v, want status=%s output_gated=false", final.Status, final.OutputGated, c.want)
			}
			if !strings.Contains(auditLog, `"output_auto_released_empty"`) {
				t.Errorf("audit log has no output_auto_released_empty event:\n%s", auditLog)
			}
		})
	}
}

// The control: output that has content stays gated at every published state,
// which also shows the seam can see a gated, settled request.
func TestExecuteRequestKeepsGatedOutputWithContentGated(t *testing.T) {
	cases := []struct {
		name    string
		command string
		args    []string
	}{
		{"stdout", "echo", []string{"gated-content"}},
		{"stderr only", "sh", []string{"-c", "echo gated-content >&2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, id, seen, auditLog := runGated(t, c.command, c.args...)

			sawSettled := false
			for _, p := range seen {
				if !p.settled() {
					continue
				}
				sawSettled = true
				if !p.gated {
					t.Errorf("request %s with output was published ungated at log line %q", id, p.line)
				}
			}
			if !sawSettled {
				t.Fatalf("no log line was written after the result was stored (saw %+v)", seen)
			}
			if final := s.Get(id); !final.OutputGated {
				t.Errorf("final output_gated=false for a result with content; want it gated until released")
			}
			if strings.Contains(auditLog, `"output_auto_released_empty"`) {
				t.Errorf("a result with content was audited as auto-released:\n%s", auditLog)
			}
		})
	}
}
