package integration

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// contentTailSentinel is the last line of the fixture. write_file caps the
// approval-card preview at 2048 bytes (mcp/tools.go), so anything that returns
// a prefix loses this line — which is what makes it a truncation detector
// rather than a size check.
const contentTailSentinel = "__TAIL_SENTINEL_9f3a__"

func bigWriteFileContent() string {
	var b strings.Builder
	for i := 0; b.Len() < 5120; i++ {
		fmt.Fprintf(&b, "line %04d: the approver must be able to read this whole file\n", i)
	}
	b.WriteString(contentTailSentinel)
	return b.String()
}

// TestWriteFileContentRouteServesWholeFile drives the real relay: queue a
// write_file larger than the preview cap, then read the bytes back over the
// dashboard's content route and assert nothing was lost. This is the end-to-end
// answer to "the approver can only see the first 2048 bytes of what they are
// about to approve."
func TestWriteFileContentRouteServesWholeFile(t *testing.T) {
	s, c := initClient(t)

	content := bigWriteFileContent()
	if len(content) <= 2048 {
		t.Fatalf("fixture is %d bytes — it must exceed the 2048-byte preview cap to test anything", len(content))
	}

	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"host":    "192.168.10.50",
			"path":    "/opt/relay/big.conf",
			"content": content,
			"reason":  "Deploy a config bigger than the approval preview",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error queuing write_file")
	}
	wfr := extractWriteFileResponse(t, resp)

	// The card really is truncated — this is the problem the route solves.
	found := findRequestByID(t, c, 3, wfr.RequestID)
	if strings.Contains(found.Reason, contentTailSentinel) {
		t.Errorf("approval reason unexpectedly carries the tail sentinel — the preview cap moved; "+
			"this test's premise (reason is a preview, not the bytes) no longer holds (reason %d bytes)", len(found.Reason))
	}
	if found.StdinLen != len(content) {
		t.Errorf("stdin_len = %d, want %d", found.StdinLen, len(content))
	}

	contentURL := fmt.Sprintf("%s/api/requests/%s/content", s.WebURL(), wfr.RequestID)

	status, body := WebGet(t, contentURL, s.token)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 — the approver has no way to read past the preview", contentURL, status)
	}
	if string(body) != content {
		t.Fatalf("content route returned %d of %d bytes, tail sentinel present=%v — TRUNCATION REGRESSED "+
			"(a cap on the /api/requests/{id}/content path produces exactly this)",
			len(body), len(content), strings.HasSuffix(string(body), contentTailSentinel))
	}

	// The route serves agent-supplied bytes, so it must not be sniffable as HTML.
	resp2 := WebGetResp(t, contentURL, s.token)
	defer resp2.Body.Close()
	if ct := resp2.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
	}
	if nos := resp2.Header.Get("X-Content-Type-Options"); nos != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nos)
	}
	if sum := resp2.Header.Get("X-Stdin-SHA256"); sum == "" || sum != found.StdinSHA256 {
		t.Errorf("X-Stdin-SHA256 = %q, want the request's stdin_sha256 %q", sum, found.StdinSHA256)
	}

	// Deny so nothing SSHes anywhere.
	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}

// TestWriteFileContentRouteRequiresAuth pins that the new route inherited the
// dashboard's bearer auth. File content is exactly the payload that must not
// leak to an unauthenticated caller — /events is deliberately open, this is not.
func TestWriteFileContentRouteRequiresAuth(t *testing.T) {
	s, c := initClient(t)

	content := bigWriteFileContent()
	resp := c.Call(t, 2, "tools/call", map[string]interface{}{
		"name": "write_file",
		"arguments": map[string]interface{}{
			"host":    "192.168.10.50",
			"path":    "/opt/relay/secret.conf",
			"content": content,
			"reason":  "Deploy a config",
		},
	})
	if isErrorResponse(resp) {
		t.Fatal("unexpected error queuing write_file")
	}
	wfr := extractWriteFileResponse(t, resp)

	contentURL := fmt.Sprintf("%s/api/requests/%s/content", s.WebURL(), wfr.RequestID)

	// Control first: a 401 means nothing unless the same URL answers 200 with
	// the token. Without this the assertion below passes identically whether
	// auth works or the route simply does not exist.
	if status, _ := WebGet(t, contentURL, s.token); status != http.StatusOK {
		t.Fatalf("authenticated GET %s = %d, want 200 — control failed, so the 401 below would prove nothing",
			contentURL, status)
	}

	unauth := WebGetResp(t, contentURL, "")
	defer unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET %s = %d, want 401 — file content must not be readable without the token",
			contentURL, unauth.StatusCode)
	}

	WebPost(t,
		fmt.Sprintf("%s/api/requests/%s/deny", s.WebURL(), wfr.RequestID),
		s.token, map[string]string{"reason": "test only"})
}
