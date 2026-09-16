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

// missingBearerHint is the 401 body for a caller that presented no credential
// at all — no bearer scheme, or the scheme with an empty token. It names the
// actual fix, which is safe precisely because the request carried no
// credential: nothing here reports on whether any particular token is valid.
//
// The gap it closes: the relay requires a bearer token on every request, and a
// client that cannot attach one — an SSE wrapper with no header option, or a
// config that simply omits it — cannot authenticate against this port no
// matter which token it holds. It used to learn that as a bare "unauthorized".
const missingBearerHint = `unauthorized: this endpoint needs an "Authorization: Bearer <token>" header, and this request sent none.

oh lmao yea, you probably need the systemd service — sorry bb.

If your MCP client cannot set a custom header, it cannot authenticate here directly,
with any token. Route it through the loopback proxy instead — the proxy injects the
header on every request:

    systemctl start relay-mcp-proxy.service

then point the client at the proxy rather than at this relay:

    http://127.0.0.1:8099/sse

A client that CAN set headers (curl, a native MCP SDK) should just send the header itself.`

// bearerScheme is the auth-scheme this endpoint accepts. Matching stays
// case-sensitive, as it has always been here.
const bearerScheme = "Bearer"

// AuthMiddleware gates next behind the verifier. On success the client name is
// attached to the request context. A failure is always 401, but the body
// splits on why:
//
//   - No credential was presented at all — either no bearer scheme, or the
//     scheme with an empty token: the caller is misconfigured, so the response
//     explains how to configure it (missingBearerHint).
//   - A non-empty bearer token WAS presented and did not verify: a bare
//     "unauthorized", identical whether the token is unknown or belongs to a
//     revoked client. That indistinguishability is the security property; do
//     not weaken it.
func AuthMiddleware(v Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")

		// Two separate questions, both asked out loud.
		//
		// 1. Did the caller use the bearer scheme at all? A bare "Bearer" with
		//    nothing behind it counts as having used it.
		usesBearer := header == bearerScheme || strings.HasPrefix(header, bearerScheme+" ")
		// 2. Is there a token behind the scheme? Trim before asking, so the
		//    answer does not depend on how this particular HTTP stack
		//    normalizes whitespace. net/http strips trailing OWS (RFC 7230), so
		//    a wire header of exactly "Authorization: Bearer " reaches us as
		//    "Bearer" — but it is the check below that decides the outcome, not
		//    that stdlib behavior, and it holds just as well on a stack that
		//    trims nothing.
		provided := strings.TrimSpace(strings.TrimPrefix(header, bearerScheme))

		// Case (a): no credential was presented. An empty token is not a failed
		// credential attempt — it is the same "never actually tried to
		// authenticate" case as sending no header at all, so it gets the same
		// help. Safe either way: a request carrying no credential cannot be
		// told anything about credential validity.
		if !usesBearer || provided == "" {
			http.Error(w, missingBearerHint, http.StatusUnauthorized)
			return
		}

		client, ok := v.Verify(provided)
		if !ok {
			// Case (b): credential validity — must never leak which of unknown
			// or revoked it was. Bare body, unchanged.
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
