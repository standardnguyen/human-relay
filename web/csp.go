package web

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

// The dashboard and /chat hold the approver token in localStorage and render
// agent-supplied strings, so a script injected into either page approves with
// the one credential agents must never wield. Neither page builds inline event
// handlers any more; this policy is the second, independent layer: script-src
// is a fresh per-response nonce with no 'unsafe-inline', so an injected
// onclick/onmouseover or <script> cannot run even if some render path fails
// to escape. Inline style attributes stay allowed (the pages use them, and a
// style cannot call the API).
func pageCSP(nonce string) string {
	return "default-src 'self'; " +
		"script-src 'nonce-" + nonce + "'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'self'"
}

// pageData is what the page templates receive.
type pageData struct {
	Nonce string
}

// servePage renders one of the HTML templates under a fresh CSP nonce.
func (h *Handler) servePage(w http.ResponseWriter, name string) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	nonce := base64.RawURLEncoding.EncodeToString(b[:])
	w.Header().Set("Content-Security-Policy", pageCSP(nonce))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.tmpl.ExecuteTemplate(w, name, pageData{Nonce: nonce})
}
