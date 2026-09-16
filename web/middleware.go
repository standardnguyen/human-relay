package web

import (
	"net/http"
	"strings"

	"github.com/standardnguyen/human-relay/auth"
)

// Verifier authenticates a presented bearer token and returns the name of the
// client it belongs to. auth.Verifier implements it.
type Verifier interface {
	Verify(token string) (client string, ok bool)
}

// missingBearerHint is the 401 body for a caller that never attempted the
// bearer scheme at all. It names the actual fix, which is safe precisely
// because the request carried no credential: nothing here reports on whether
// any particular token is valid.
//
// The gap it closes: the relay requires a bearer token, but mcp-remote — the
// SSE wrapper most harnesses use — cannot attach a custom header, so such a
// client cannot authenticate against this port directly no matter which token
// it holds. It used to learn that as a bare "unauthorized".
const missingBearerHint = `unauthorized: this endpoint needs an "Authorization: Bearer <token>" header, and this request sent none.

oh lmao yea, you probably need the systemd service — sorry bb.

If your MCP client is mcp-remote (or anything else that cannot set a custom header),
it cannot authenticate here directly, with any token. Route it through the loopback
proxy instead — the proxy injects the header on every request:

    systemctl start relay-mcp-proxy.service

then point the client at the proxy rather than at this relay:

    http://127.0.0.1:8099/sse

A client that CAN set headers (curl, a native MCP SDK) should just send the header itself.`

// AuthMiddleware gates next behind the verifier. On success the client name is
// attached to the request context. A failure is always 401, but the body
// splits on why:
//
//   - No bearer scheme presented at all: the caller is misconfigured, so the
//     response explains how to configure it (missingBearerHint).
//   - A bearer token WAS presented and did not verify: a bare "unauthorized",
//     identical whether the token is empty, unknown, or belongs to a revoked
//     client. That indistinguishability is the security property; do not
//     weaken it.
func AuthMiddleware(v Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		// Case (a): client misconfiguration — safe to explain, since a request
		// with no credential cannot be told anything about credential validity.
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, missingBearerHint, http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(header, "Bearer ")
		client, ok := v.Verify(provided)
		if !ok {
			// Case (b): credential validity — must never leak which of empty,
			// unknown, or revoked it was. Bare body, unchanged.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithClient(r.Context(), client)))
	})
}

func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only check mutations
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No origin header — allow (non-browser clients like curl)
			next.ServeHTTP(w, r)
			return
		}
		// For browser requests, Origin must match the host
		host := r.Host
		if host == "" {
			host = r.URL.Host
		}
		// Accept if origin contains the host (handles port differences)
		if !strings.Contains(origin, host) {
			// Also accept same-origin where origin host matches
			// Parse just enough to compare
			originHost := strings.TrimPrefix(origin, "http://")
			originHost = strings.TrimPrefix(originHost, "https://")
			originHost = strings.Split(originHost, "/")[0]
			// Strip port from host for comparison
			hostNoPort := strings.Split(host, ":")[0]
			originNoPort := strings.Split(originHost, ":")[0]
			if hostNoPort != originNoPort {
				http.Error(w, "CSRF validation failed", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
