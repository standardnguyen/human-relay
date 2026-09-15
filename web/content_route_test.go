package web

// Tests for GET /api/requests/{id}/content — the route that hands the approver
// the exact bytes a write_file request will pipe into `cat > path`.
//
// The approval card only ever shows a *preview*: mcp/tools.go caps the reason
// string at 2048 bytes before the request reaches the store, so a 12 KB config
// file is reviewed one sixth at a time and the rest is unreachable over HTTP.
// The real bytes live in store.Request.Stdin (json:"-", so /api/requests can't
// carry them). This route is the only way to read them, and these tests pin
// that it returns them whole.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/standardnguyen/human-relay/store"
)

// tailSentinel is the last line of the fixture content. A truncating route
// returns a prefix, so the sentinel is the cheapest positive proof that the
// tail survived — and it is unique, so it cannot be matched by accident.
const tailSentinel = "__TAIL_SENTINEL_9f3a__"

// contentFixture builds a body larger than every cap on this path (2048 in
// write_file, 4096 in create_script/create_then_run) whose final line is the
// sentinel.
func contentFixture() []byte {
	var b strings.Builder
	for i := 0; b.Len() < 5120; i++ {
		fmt.Fprintf(&b, "line %04d: the approver must be able to read this whole file\n", i)
	}
	b.WriteString(tailSentinel)
	return []byte(b.String())
}

// newContentTestServer wires a real Handler over a real store and returns the
// httptest server plus the id of a pending request carrying content as stdin.
func newContentTestServer(t *testing.T, content []byte) (*httptest.Server, string) {
	t.Helper()
	s := store.New()
	h := NewHandler(s, nil, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req := s.AddWithStdin("ssh", []string{"root@192.168.10.50", "--", "cat > '/etc/thing.conf'"},
		"[FILE 5142B -> 192.168.10.50:/etc/thing.conf] deploy config", "", false, 30, content, "test-client")
	return srv, req.ID
}

func TestRequestContentRouteServesFullStdin(t *testing.T) {
	content := contentFixture()
	srv, id := newContentTestServer(t, content)

	resp, err := http.Get(srv.URL + "/api/requests/" + id + "/content")
	if err != nil {
		t.Fatalf("GET content: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/requests/%s/content = %d, want 200 — the content route is missing, so the approver cannot read past the %d-byte preview",
			id, resp.StatusCode, 2048)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/plain; charset=utf-8")
	}
	if nos := resp.Header.Get("X-Content-Type-Options"); nos != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff — agent-supplied bytes must never be sniffed as HTML", nos)
	}
	if sum := resp.Header.Get("X-Stdin-SHA256"); sum != store.StdinDigest(content) {
		t.Errorf("X-Stdin-SHA256 = %q, want %q", sum, store.StdinDigest(content))
	}

	body := readAll(t, resp)
	if !bytes.Equal(body, content) {
		t.Errorf("content route returned %d bytes, want %d — bytes differ from the stdin the executor will pipe", len(body), len(content))
	}
}

// TestRequestContentRouteNoTruncation is the sabotage pin: any re-introduced
// [:N] slice on this path fails here and the message names the offender.
func TestRequestContentRouteNoTruncation(t *testing.T) {
	content := contentFixture()
	srv, id := newContentTestServer(t, content)

	resp, err := http.Get(srv.URL + "/api/requests/" + id + "/content")
	if err != nil {
		t.Fatalf("GET content: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET content = %d, want 200 (route missing)", resp.StatusCode)
	}
	body := readAll(t, resp)

	if len(body) != len(content) || !bytes.HasSuffix(body, []byte(tailSentinel)) {
		t.Fatalf("content route returned %d of %d bytes, tail sentinel present=%v — TRUNCATION REGRESSED "+
			"(a cap at 2048/4096/1MB on the /api/requests/{id}/content path produces exactly this); "+
			"the approver is being shown a prefix of what will actually be written",
			len(body), len(content), bytes.HasSuffix(body, []byte(tailSentinel)))
	}
}

func TestRequestContentRouteMethodAndID(t *testing.T) {
	srv, id := newContentTestServer(t, contentFixture())

	t.Run("post_not_allowed", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/api/requests/"+id+"/content", "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatalf("POST content: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST content = %d, want 405 — the content route is read-only", resp.StatusCode)
		}
	})

	t.Run("unknown_id_404", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/requests/deadbeefdeadbeef/content")
		if err != nil {
			t.Fatalf("GET bogus content: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET content for unknown id = %d, want 404", resp.StatusCode)
		}
	})
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return buf.Bytes()
}
