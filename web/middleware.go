package web

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func AuthMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
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
