package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/standardnguyen/human-relay/store"
)

func newTestExecutor() *Executor {
	return New(Config{
		DefaultTimeout: 10,
		MaxTimeout:     30,
	})
}

func TestExecuteHTTPGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, `{"id":"abc123","name":"My Board"}`)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL + "/1/boards/abc",
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if result.StatusCode != 200 {
		t.Fatalf("expected status 200, got %d", result.StatusCode)
	}
	if !strings.Contains(result.Stdout, "abc123") {
		t.Fatalf("expected response body with id, got: %s", result.Stdout)
	}
	if result.RespHeaders["Content-Type"] != "application/json" {
		t.Fatalf("expected Content-Type header, got: %v", result.RespHeaders)
	}
}

func TestExecuteHTTPPostWithBody(t *testing.T) {
	var receivedBody string
	var receivedContentType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"card123"}`)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "POST",
		HTTPURL:    ts.URL + "/1/cards",
		HTTPHeaders: map[string]string{
			"Content-Type": "application/json",
		},
		HTTPBody: `{"idList":"list1","name":"Test Card"}`,
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.StatusCode != 201 {
		t.Fatalf("expected status 201, got %d", result.StatusCode)
	}
	if receivedContentType != "application/json" {
		t.Fatalf("expected Content-Type to be forwarded, got %q", receivedContentType)
	}
	if !strings.Contains(receivedBody, "Test Card") {
		t.Fatalf("expected body to be forwarded, got %q", receivedBody)
	}
}

func TestExecuteHTTPHeaders(t *testing.T) {
	var receivedAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		fmt.Fprint(w, "ok")
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL,
		HTTPHeaders: map[string]string{
			"Authorization": "Bearer my-secret-token",
			"X-Custom":      "custom-value",
		},
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if receivedAuth != "Bearer my-secret-token" {
		t.Fatalf("expected auth header forwarded, got %q", receivedAuth)
	}
}

func TestExecuteHTTP4xxReturnsExitCode1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":"not found"}`)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL + "/missing",
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 1 {
		t.Fatalf("expected exit code 1 for 404, got %d", result.ExitCode)
	}
	if result.StatusCode != 404 {
		t.Fatalf("expected status 404, got %d", result.StatusCode)
	}
	if !strings.Contains(result.Stdout, "not found") {
		t.Fatalf("response body should still be captured: %s", result.Stdout)
	}
}

func TestExecuteHTTP5xxReturnsExitCode1(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "internal server error")
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "POST",
		HTTPURL:    ts.URL,
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 1 {
		t.Fatalf("expected exit code 1 for 500, got %d", result.ExitCode)
	}
	if result.StatusCode != 500 {
		t.Fatalf("expected status 500, got %d", result.StatusCode)
	}
}

func TestExecuteHTTPTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the request context is cancelled (executor timeout)
		<-r.Context().Done()
	}))
	defer ts.Close()

	e := New(Config{
		DefaultTimeout: 1, // 1 second
		MaxTimeout:     5,
	})
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL,
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != -1 {
		t.Fatalf("expected exit code -1 for timeout, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "timed out") {
		t.Fatalf("expected timeout error, got: %s", result.Stderr)
	}
}

func TestExecuteHTTPBadURL(t *testing.T) {
	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    "://invalid-url",
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != -1 {
		t.Fatalf("expected exit code -1 for bad URL, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "failed to create request") {
		t.Fatalf("expected creation error, got: %s", result.Stderr)
	}
}

func TestExecuteHTTPConnectionRefused(t *testing.T) {
	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    "http://127.0.0.1:1", // port 1 should be refused
		Timeout:    2,
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != -1 {
		t.Fatalf("expected exit code -1 for connection error, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Stderr, "HTTP request failed") {
		t.Fatalf("expected request failed error, got: %s", result.Stderr)
	}
}

func TestExecuteHTTPNoBodyForGet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			buf := make([]byte, 1)
			n, _ := r.Body.Read(buf)
			if n > 0 {
				t.Fatal("GET request should not have a body")
			}
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL,
		HTTPBody:   "", // empty body
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestExecuteHTTPPutMethod(t *testing.T) {
	var receivedMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(200)
		fmt.Fprint(w, `{"updated":true}`)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "PUT",
		HTTPURL:    ts.URL + "/1/cards/abc",
		HTTPHeaders: map[string]string{
			"Content-Type": "application/json",
		},
		HTTPBody: `{"name":"Updated Card"}`,
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if receivedMethod != "PUT" {
		t.Fatalf("expected PUT method, got %s", receivedMethod)
	}
}

func TestExecuteHTTPDeleteMethod(t *testing.T) {
	var receivedMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(204)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "DELETE",
		HTTPURL:    ts.URL + "/1/cards/abc",
	}

	result := e.ExecuteHTTP(req)

	// 204 is 2xx, so exit code should be 0
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0 for 204, got %d", result.ExitCode)
	}
	if result.StatusCode != 204 {
		t.Fatalf("expected status 204, got %d", result.StatusCode)
	}
	if receivedMethod != "DELETE" {
		t.Fatalf("expected DELETE method, got %s", receivedMethod)
	}
}

func TestExecuteHTTP3xxExitCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(304)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL,
	}

	result := e.ExecuteHTTP(req)

	// 304 is not 2xx, so exit code 1
	if result.ExitCode != 1 {
		t.Fatalf("expected exit code 1 for 304, got %d", result.ExitCode)
	}
	if result.StatusCode != 304 {
		t.Fatalf("expected status 304, got %d", result.StatusCode)
	}
}

func TestExecuteHTTPRespHeadersCollected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-12345")
		w.Header().Set("X-Rate-Limit-Remaining", "99")
		w.WriteHeader(200)
		fmt.Fprint(w, "ok")
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL,
	}

	result := e.ExecuteHTTP(req)

	if result.RespHeaders["X-Request-Id"] != "req-12345" {
		t.Fatalf("expected X-Request-Id header, got: %v", result.RespHeaders)
	}
	if result.RespHeaders["X-Rate-Limit-Remaining"] != "99" {
		t.Fatalf("expected rate limit header, got: %v", result.RespHeaders)
	}
}

func TestExecuteHTTPEnvVarExpansion(t *testing.T) {
	// Set test env vars
	t.Setenv("TEST_API_KEY", "key123")
	t.Setenv("TEST_TOKEN", "tok456")

	var receivedURL string
	var receivedAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		fmt.Fprint(w, "ok")
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL + "/1/boards?key=${TEST_API_KEY}&token=${TEST_TOKEN}",
		HTTPHeaders: map[string]string{
			"Authorization": "Bearer ${TEST_TOKEN}",
		},
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(receivedURL, "key=key123") {
		t.Fatalf("expected expanded API key in URL, got: %s", receivedURL)
	}
	if !strings.Contains(receivedURL, "token=tok456") {
		t.Fatalf("expected expanded token in URL, got: %s", receivedURL)
	}
	if receivedAuth != "Bearer tok456" {
		t.Fatalf("expected expanded auth header, got: %s", receivedAuth)
	}
}

func TestExecuteHTTPEnvVarInBody(t *testing.T) {
	t.Setenv("TEST_SECRET", "s3cret")

	var receivedBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(200)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "POST",
		HTTPURL:    ts.URL,
		HTTPBody:   `{"secret":"${TEST_SECRET}"}`,
	}

	e.ExecuteHTTP(req)

	if !strings.Contains(receivedBody, "s3cret") {
		t.Fatalf("expected expanded secret in body, got: %s", receivedBody)
	}
}

func TestExecuteHTTPUnsetEnvVarExpandsEmpty(t *testing.T) {
	var receivedURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(200)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL + "/test?key=${DEFINITELY_NOT_SET_12345}",
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if strings.Contains(receivedURL, "${") {
		t.Fatalf("placeholder should have been expanded (to empty), got: %s", receivedURL)
	}
	if !strings.Contains(receivedURL, "key=") {
		t.Fatalf("expected key= with empty value, got: %s", receivedURL)
	}
}

func TestExecuteHTTPNoExpansionWithoutPlaceholder(t *testing.T) {
	var receivedURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(200)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL + "/plain?key=literal_value",
	}

	e.ExecuteHTTP(req)

	if !strings.Contains(receivedURL, "key=literal_value") {
		t.Fatalf("literal value should be preserved, got: %s", receivedURL)
	}
}

func TestExecuteHTTPErrorScrubsCredentials(t *testing.T) {
	t.Setenv("TEST_SECRET_KEY", "supersecret123")

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    "http://nonexistent.invalid/api?key=${TEST_SECRET_KEY}",
		Timeout:    2,
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != -1 {
		t.Fatalf("expected error, got exit code %d", result.ExitCode)
	}
	if strings.Contains(result.Stderr, "supersecret123") {
		t.Fatalf("expanded credential leaked in error message: %s", result.Stderr)
	}
	// The unexpanded placeholder should be there instead
	if !strings.Contains(result.Stderr, "${TEST_SECRET_KEY}") {
		t.Fatalf("expected unexpanded placeholder in error, got: %s", result.Stderr)
	}
}

// Finding #25a: ${MHR_AUTH_TOKEN} must never expand. The store keeps the
// unexpanded string for review, so a request naming the relay's own bearer
// secret looks harmless to the human and would otherwise ship the real token.
func TestExecuteHTTPAuthTokenNeverExpanded(t *testing.T) {
	t.Setenv("MHR_AUTH_TOKEN", "relay-bearer-supersecret")

	var receivedURL, receivedHeader, receivedBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		receivedHeader = r.Header.Get("X-Exfil")
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(200)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "POST",
		HTTPURL:    ts.URL + "/collect?t=${MHR_AUTH_TOKEN}",
		HTTPHeaders: map[string]string{
			"X-Exfil": "Bearer ${MHR_AUTH_TOKEN}",
		},
		HTTPBody: `{"token":"${MHR_AUTH_TOKEN}"}`,
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	for _, tc := range []struct{ name, got string }{
		{"url", receivedURL},
		{"header", receivedHeader},
		{"body", receivedBody},
	} {
		if strings.Contains(tc.got, "relay-bearer-supersecret") {
			t.Fatalf("relay auth token leaked via %s: %s", tc.name, tc.got)
		}
		if !strings.Contains(tc.got, "MHR_AUTH_TOKEN") {
			t.Fatalf("expected literal placeholder preserved in %s, got: %s", tc.name, tc.got)
		}
	}
	if receivedHeader != "Bearer ${MHR_AUTH_TOKEN}" {
		t.Fatalf("expected header placeholder left literal, got: %q", receivedHeader)
	}
	if !strings.Contains(receivedBody, "${MHR_AUTH_TOKEN}") {
		t.Fatalf("expected body placeholder left literal, got: %q", receivedBody)
	}
}

// The denylist is narrow on purpose: other env-var secrets must still expand,
// which is the feature's intended use.
func TestExecuteHTTPNonProtectedVarsStillExpand(t *testing.T) {
	t.Setenv("MHR_AUTH_TOKEN", "relay-bearer-supersecret")
	t.Setenv("TRELLO_TOKEN", "trello-tok-789")
	t.Setenv("MHR_DATA_DIR", "/opt/human-relay/data")

	var receivedURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(200)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL + "/1/cards?token=${TRELLO_TOKEN}&dir=${MHR_DATA_DIR}",
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(receivedURL, "trello-tok-789") {
		t.Fatalf("non-protected secret should still expand, got: %s", receivedURL)
	}
	if !strings.Contains(receivedURL, "%2Fopt%2Fhuman-relay%2Fdata") && !strings.Contains(receivedURL, "/opt/human-relay/data") {
		t.Fatalf("non-secret MHR_ config var should still expand, got: %s", receivedURL)
	}
}

// Finding #25b: a redirect must not be followed. Go's default policy only
// strips Authorization/Cookie/WWW-Authenticate cross-host; any other custom
// header (holding an expanded secret) would ride along to the 3xx target.
func TestExecuteHTTPRedirectNotFollowed(t *testing.T) {
	t.Setenv("TEST_VAULTED_KEY", "vaulted-key-abc")

	var attackerHits int
	var attackerSawHeader string
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits++
		attackerSawHeader = r.Header.Get("X-Api-Key")
		w.WriteHeader(200)
		fmt.Fprint(w, "attacker got it")
	}))
	defer attacker.Close()

	benign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/steal", http.StatusFound)
	}))
	defer benign.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    benign.URL + "/looks-fine",
		HTTPHeaders: map[string]string{
			"X-Api-Key": "${TEST_VAULTED_KEY}",
		},
	}

	result := e.ExecuteHTTP(req)

	if attackerHits != 0 {
		t.Fatalf("redirect target was contacted %d time(s); it must never be reached", attackerHits)
	}
	if attackerSawHeader != "" {
		t.Fatalf("custom header forwarded to redirect target: %q", attackerSawHeader)
	}
	if result.StatusCode != http.StatusFound {
		t.Fatalf("expected the 302 itself to be returned, got status %d", result.StatusCode)
	}
	if result.ExitCode != 1 {
		t.Fatalf("expected exit code 1 for a 302, got %d", result.ExitCode)
	}
	if loc := result.RespHeaders["Location"]; loc != attacker.URL+"/steal" {
		t.Fatalf("expected Location header surfaced for inspection, got: %q", loc)
	}
}

// Same-host redirects are not followed either — the reviewer approved one URL.
func TestExecuteHTTPSameHostRedirectNotFollowed(t *testing.T) {
	var secondHits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/second" {
			secondHits++
			w.WriteHeader(200)
			return
		}
		http.Redirect(w, r, "/second", http.StatusMovedPermanently)
	}))
	defer ts.Close()

	e := newTestExecutor()
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL + "/first",
	}

	result := e.ExecuteHTTP(req)

	if secondHits != 0 {
		t.Fatalf("redirect was followed: /second hit %d time(s)", secondHits)
	}
	if result.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("expected 301 returned, got %d", result.StatusCode)
	}
}

func TestExecuteHTTPTimeoutOverride(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	e := New(Config{
		DefaultTimeout: 30,
		MaxTimeout:     60,
	})
	req := &store.Request{
		Type:       "http",
		HTTPMethod: "GET",
		HTTPURL:    ts.URL,
		Timeout:    1, // override to 1s
	}

	result := e.ExecuteHTTP(req)

	if result.ExitCode != -1 {
		t.Fatalf("expected timeout, got exit code %d", result.ExitCode)
	}
}
