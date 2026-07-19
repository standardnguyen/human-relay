package integration

import (
	"strings"
	"testing"
)

// The chat page is a public HTML surface (like the dashboard) — the token is
// entered client-side and used only for the API calls the page makes.
func TestChatPageServedPublicly(t *testing.T) {
	s := StartServer(t)

	code, body := WebGet(t, s.WebURL()+"/chat", "")
	if code != 200 {
		t.Fatalf("expected /chat to be served without auth (200), got %d", code)
	}
	html := string(body)
	for _, marker := range []string{"Signet Gate", "/api/requests", "signal-", "mapRequest"} {
		if !strings.Contains(html, marker) {
			t.Errorf("chat page missing expected marker %q", marker)
		}
	}
}

func TestChatPageOnlyGet(t *testing.T) {
	s := StartServer(t)

	// A POST to /chat is not the public page path; it must fall through to auth.
	code, _ := WebPost(t, s.WebURL()+"/chat", "", nil)
	if code == 200 {
		t.Errorf("POST /chat should not be served as the public page; got 200")
	}
}
