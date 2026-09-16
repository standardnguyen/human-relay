package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/standardnguyen/human-relay/auth"
)

// genericUnauthorizedBody is the bare 401 body a caller gets when it presented a
// bearer token that did not verify. Pinned as a literal so a change to it has to
// be made here, deliberately.
const genericUnauthorizedBody = "unauthorized\n"

// stubVerifier accepts exactly one token and reports the client name it was
// configured with.
type stubVerifier struct {
	token  string
	client string
}

func (v stubVerifier) Verify(token string) (string, bool) {
	if token == v.token {
		return v.client, true
	}
	return "", false
}

func TestAuthMiddlewareAttachesClientName(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = auth.ClientFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := AuthMiddleware(stubVerifier{token: "s3cret", client: "cc-115"}, next)

	req := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if seen != "cc-115" {
		t.Fatalf("context client = %q, want cc-115", seen)
	}
}

func TestAuthMiddlewareRejectsBadTokens(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("downstream handler ran for a rejected request")
	})
	h := AuthMiddleware(stubVerifier{token: "s3cret", client: "cc-115"}, next)

	// Every case is a 401, but the body splits on why: (a) no bearer scheme was
	// attempted at all, which is client misconfiguration and safe to explain;
	// (b) a token was presented and did not verify, which is a question about
	// credential validity and must never leak which token was wrong.
	cases := []struct {
		name     string
		header   string
		wantHelp bool
	}{
		{"missing header", "", true},
		{"wrong scheme", "Basic s3cret", true},
		{"unknown token", "Bearer nope", false},
		// "Bearer " pins the middleware's own contract on the header value. Note
		// that net/http trims trailing OWS (RFC 7230), so a request that puts
		// exactly this on the wire arrives here as "Bearer" and lands in case (a)
		// instead -- which leaks nothing, since an empty token is not a credential.
		{"empty bearer", "Bearer ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			got := rec.Body.String()

			if tc.wantHelp {
				// Case (a). The caller never attempted the scheme, so nothing here
				// reports on any token's validity — name the actual fix instead.
				for _, want := range []string{"Bearer", "relay-mcp-proxy.service", "127.0.0.1:8099"} {
					if !strings.Contains(got, want) {
						t.Fatalf("body = %q, want it to mention %q", got, want)
					}
				}
				if got == genericUnauthorizedBody {
					t.Fatalf("body is the bare %q; a caller that sent no token should be told how to send one",
						genericUnauthorizedBody)
				}
				return
			}

			// Case (b). Revoked and unknown must be indistinguishable.
			if got != genericUnauthorizedBody {
				t.Fatalf("body = %q, want %q", got, genericUnauthorizedBody)
			}
		})
	}
}

// TestAuthMiddlewareFailureBodiesDiffer pins the split itself rather than each
// side of it. Asserting the two cases in separate subtests would still pass if
// both regressed to one shared body, so the distinction is what is under test:
// a caller that sent no token learns how to configure one, and a caller whose
// token failed to verify learns nothing at all.
func TestAuthMiddlewareFailureBodiesDiffer(t *testing.T) {
	h := AuthMiddleware(
		stubVerifier{token: "s3cret", client: "cc-115"},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("downstream handler ran for a rejected request")
		}),
	)

	respond := func(header string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	noSchemeCode, noScheme := respond("")            // case (a)
	badTokenCode, badToken := respond("Bearer nope") // case (b)

	if noSchemeCode != http.StatusUnauthorized || badTokenCode != http.StatusUnauthorized {
		t.Fatalf("status = %d (no scheme) and %d (bad token), want 401 for both",
			noSchemeCode, badTokenCode)
	}
	if noScheme == badToken {
		t.Fatalf("both failures answered with %q — the split is the point: "+
			"misconfiguration is explainable, credential validity is not", noScheme)
	}
	if badToken != genericUnauthorizedBody {
		t.Fatalf("bad-token body = %q, want the bare %q", badToken, genericUnauthorizedBody)
	}

	// ...and within case (b) the bodies must stay identical to each other, so a
	// caller cannot probe which of empty/unknown/revoked it hit.
	_, otherBadToken := respond("Bearer some-other-wrong-token")
	_, emptyToken := respond("Bearer ")
	if otherBadToken != badToken || emptyToken != badToken {
		t.Fatalf("invalid-token bodies differ: %q, %q, %q — they must be indistinguishable",
			badToken, otherBadToken, emptyToken)
	}
}
