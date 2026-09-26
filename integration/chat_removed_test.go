package integration

import (
	"strings"
	"testing"
)

// TestChatPageRemoved: /chat was served without auth as a public page. It is
// gone, so an unauthenticated GET now goes through auth like any other path
// that is not the dashboard, and an authenticated one finds nothing there.
func TestChatPageRemoved(t *testing.T) {
	s := StartServer(t)

	code, body := WebGet(t, s.WebURL()+"/chat", "")
	if code != 401 {
		t.Fatalf("GET /chat without auth: status %d, want 401; body starts %q", code, firstN(body, 80))
	}
	code, body = WebGet(t, s.WebURL()+"/chat", s.token)
	if code != 404 {
		t.Fatalf("GET /chat with a token: status %d, want 404; body starts %q", code, firstN(body, 80))
	}

	// Control: the dashboard itself is still served without auth.
	code, body = WebGet(t, s.WebURL()+"/", "")
	if code != 200 || !strings.Contains(string(body), "<html") {
		t.Fatalf("control: GET / without auth: status %d", code)
	}
}

func firstN(b []byte, n int) string {
	if len(b) < n {
		return string(b)
	}
	return string(b[:n])
}
