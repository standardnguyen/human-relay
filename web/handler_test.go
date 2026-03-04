package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"git.ekaterina.net/administrator/human-relay/executor"
	"git.ekaterina.net/administrator/human-relay/store"
)

func newTestHandler(t *testing.T) (*Handler, *http.ServeMux) {
	t.Helper()
	s := store.New()
	exec := executor.New(executor.Config{
		DefaultTimeout: 10,
		MaxTimeout:     30,
	})
	h := NewHandler(s, exec)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

func addPendingRequest(h *Handler) string {
	r := h.store.Add("echo", []string{"hello"}, "test", "", false, 10)
	return r.ID
}

func TestApproveSucceeds(t *testing.T) {
	h, mux := newTestHandler(t)
	id := addPendingRequest(h)

	req := httptest.NewRequest(http.MethodPost, "/api/requests/"+id+"/approve", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "approved" {
		t.Fatalf("expected approved, got %s", resp["status"])
	}
}

func TestApproveBlockedByCooldown(t *testing.T) {
	h, mux := newTestHandler(t)

	// First approval
	id1 := addPendingRequest(h)
	req1 := httptest.NewRequest(http.MethodPost, "/api/requests/"+id1+"/approve", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first approve: expected 200, got %d", w1.Code)
	}

	// Second approval immediately — should be blocked
	id2 := addPendingRequest(h)
	req2 := httptest.NewRequest(http.MethodPost, "/api/requests/"+id2+"/approve", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second approve: expected 429, got %d", w2.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&resp)
	if resp["error"] != "cooldown active" {
		t.Fatalf("expected cooldown error, got %v", resp)
	}
	remainMs, ok := resp["remaining_ms"].(float64)
	if !ok || remainMs <= 0 {
		t.Fatalf("expected positive remaining_ms, got %v", resp["remaining_ms"])
	}
	// Retry-After header should be present
	retryAfter := w2.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("expected Retry-After header")
	}

	// Verify the second request is still pending (wasn't approved)
	r2 := h.store.Get(id2)
	if r2.Status != store.StatusPending {
		t.Fatalf("expected request to remain pending, got %s", r2.Status)
	}
}

func TestApproveAfterCooldownExpires(t *testing.T) {
	h, mux := newTestHandler(t)

	// Simulate a past approval that's already expired
	h.cooldownMu.Lock()
	h.lastApproval = time.Now().Add(-approvalCooldown - time.Second)
	h.cooldownMu.Unlock()

	id := addPendingRequest(h)
	req := httptest.NewRequest(http.MethodPost, "/api/requests/"+id+"/approve", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 after cooldown expired, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDenyNotAffectedByCooldown(t *testing.T) {
	h, mux := newTestHandler(t)

	// Approve one to start cooldown
	id1 := addPendingRequest(h)
	req1 := httptest.NewRequest(http.MethodPost, "/api/requests/"+id1+"/approve", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d", w1.Code)
	}

	// Deny should still work during cooldown
	id2 := addPendingRequest(h)
	req2 := httptest.NewRequest(http.MethodPost, "/api/requests/"+id2+"/deny", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("deny during cooldown: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestListReturnsCooldownHeader(t *testing.T) {
	h, mux := newTestHandler(t)

	// No prior approval — cooldown should be 0
	req1 := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, req1)

	cdHeader := w1.Header().Get("X-Cooldown-Remaining-Ms")
	if cdHeader == "" {
		t.Fatal("expected X-Cooldown-Remaining-Ms header")
	}
	cdMs, _ := strconv.Atoi(cdHeader)
	if cdMs != 0 {
		t.Fatalf("expected 0 cooldown before any approval, got %d", cdMs)
	}

	// Approve something to start cooldown
	id := addPendingRequest(h)
	approveReq := httptest.NewRequest(http.MethodPost, "/api/requests/"+id+"/approve", nil)
	approveW := httptest.NewRecorder()
	mux.ServeHTTP(approveW, approveReq)

	// Now list should show remaining cooldown > 0
	req2 := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	cdHeader2 := w2.Header().Get("X-Cooldown-Remaining-Ms")
	cdMs2, _ := strconv.Atoi(cdHeader2)
	if cdMs2 <= 0 {
		t.Fatalf("expected positive cooldown after approval, got %d", cdMs2)
	}
	if cdMs2 > 30000 {
		t.Fatalf("cooldown should not exceed 30000ms, got %d", cdMs2)
	}
}

func TestListCooldownDecreasesOverTime(t *testing.T) {
	h, mux := newTestHandler(t)

	// Set lastApproval to 20 seconds ago — should have ~10s remaining
	h.cooldownMu.Lock()
	h.lastApproval = time.Now().Add(-20 * time.Second)
	h.cooldownMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	cdMs, _ := strconv.Atoi(w.Header().Get("X-Cooldown-Remaining-Ms"))
	// Should be roughly 10000ms (allow 9-11s window for timing)
	if cdMs < 9000 || cdMs > 11000 {
		t.Fatalf("expected ~10000ms remaining, got %d", cdMs)
	}
}

func TestListCooldownZeroAfterExpiry(t *testing.T) {
	h, mux := newTestHandler(t)

	// Set lastApproval to 31 seconds ago — cooldown should be 0
	h.cooldownMu.Lock()
	h.lastApproval = time.Now().Add(-31 * time.Second)
	h.cooldownMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	cdMs, _ := strconv.Atoi(w.Header().Get("X-Cooldown-Remaining-Ms"))
	if cdMs != 0 {
		t.Fatalf("expected 0 after expiry, got %d", cdMs)
	}
}
