package web

// The dashboard and /chat render agent-supplied strings (command, args, script
// args, inbound message fields). Both pages read the approver token from
// localStorage, so any script an agent smuggles into either page runs with the
// credential that alone may approve. Two independent defences are pinned here:
//
//  1. No inline event handler exists anywhere in either page, including the
//     HTML that the page's own JS builds as strings. Handlers are attached with
//     addEventListener and look their data up by request id, so agent text is
//     never parsed as an attribute or a JS string literal.
//  2. Each page is served with a Content-Security-Policy whose script-src is a
//     per-response nonce with no 'unsafe-inline', so even an attribute breakout
//     that slipped past (1) could not run a handler.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/standardnguyen/human-relay/store"
)

func newPageTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := NewHandler(store.New(), nil, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

var (
	nonceSrcRe  = regexp.MustCompile(`script-src 'nonce-([A-Za-z0-9+/=_-]+)'`)
	scriptTagRe = regexp.MustCompile(`<script\b[^>]*>`)
	// An inline handler attribute: whitespace or a quote, then on<event>=.
	// Matches both literal markup and markup built inside JS strings
	// ('... onclick="..."'), which is where the exploitable ones lived.
	inlineHandlerRe = regexp.MustCompile(`(?i)[\s'"/]on[a-z]+\s*=\s*["'\\]`)
)

func getPage(t *testing.T, url string) (string, http.Header) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.Header
}

func TestPages_CSPNonceForbidsInlineScript(t *testing.T) {
	srv := newPageTestServer(t)
	for _, path := range []string{"/", "/chat"} {
		t.Run(path, func(t *testing.T) {
			body, hdr := getPage(t, srv.URL+path)
			csp := hdr.Get("Content-Security-Policy")
			if csp == "" {
				t.Fatalf("%s is served without a Content-Security-Policy", path)
			}
			m := nonceSrcRe.FindStringSubmatch(csp)
			if m == nil {
				t.Fatalf("script-src is not nonce-based: %q", csp)
			}
			nonce := m[1]
			if len(nonce) < 16 {
				t.Fatalf("nonce %q is too short to be unguessable", nonce)
			}
			for _, dir := range strings.Split(csp, ";") {
				dir = strings.TrimSpace(dir)
				if strings.HasPrefix(dir, "script-src") &&
					(strings.Contains(dir, "'unsafe-inline'") || strings.Contains(dir, "'unsafe-hashes'")) {
					t.Fatalf("script-src allows inline handlers: %q", dir)
				}
			}
			if !strings.Contains(csp, "object-src 'none'") || !strings.Contains(csp, "base-uri 'none'") {
				t.Fatalf("CSP lacks object-src/base-uri lockdown: %q", csp)
			}

			tags := scriptTagRe.FindAllString(body, -1)
			if len(tags) == 0 {
				t.Fatal("control failed: page has no <script> tag, so the nonce check tests nothing")
			}
			for _, tag := range tags {
				if !strings.Contains(tag, `nonce="`+nonce+`"`) {
					t.Fatalf("script tag %q does not carry the response nonce", tag)
				}
			}

			body2, hdr2 := getPage(t, srv.URL+path)
			m2 := nonceSrcRe.FindStringSubmatch(hdr2.Get("Content-Security-Policy"))
			if m2 == nil || m2[1] == nonce {
				t.Fatal("nonce is not fresh per response")
			}
			if !strings.Contains(body2, `nonce="`+m2[1]+`"`) {
				t.Fatal("second response's script tag does not carry its own nonce")
			}
		})
	}
}

func TestPages_NoInlineEventHandlers(t *testing.T) {
	// Control: the detector must fire on the exact shapes that were exploitable.
	for _, positive := range []string{
		`<button class="btn-approve" onclick="approve('x')">`,
		`'<button class="btn-whitelist" onclick="addWhitelist(\'' + r.id + '\')">'`,
		`'<div class="convo" onclick="selectConvo(\'' + k + '\')">'`,
		`<div class="agentview" onclick="toggleAgent()">`,
		`<div onmouseover='x()'>`,
	} {
		if !inlineHandlerRe.MatchString(positive) {
			t.Fatalf("control failed: detector misses %q", positive)
		}
	}
	srv := newPageTestServer(t)
	for _, path := range []string{"/", "/chat"} {
		body, _ := getPage(t, srv.URL+path)
		if locs := inlineHandlerRe.FindAllStringIndex(body, -1); len(locs) > 0 {
			var hits []string
			for _, l := range locs {
				lo, hi := l[0]-40, l[1]+40
				if lo < 0 {
					lo = 0
				}
				if hi > len(body) {
					hi = len(body)
				}
				hits = append(hits, strings.TrimSpace(body[lo:hi]))
			}
			t.Fatalf("%s still has %d inline event handler(s); agent data can reach them:\n%s",
				path, len(locs), strings.Join(hits, "\n"))
		}
	}
}
