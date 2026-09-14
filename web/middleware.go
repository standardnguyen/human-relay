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

// AuthMiddleware gates next behind the verifier. On success the client name is
// attached to the request context; on failure the response is a bare 401
// "unauthorized" — identical whether the token is unknown, malformed, or
// belongs to a revoked client.
func AuthMiddleware(v Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(header, "Bearer ")
		client, ok := v.Verify(provided)
		if !ok {
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
