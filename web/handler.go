package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"git.ekaterina.net/administrator/human-relay/audit"
	"git.ekaterina.net/administrator/human-relay/executor"
	"git.ekaterina.net/administrator/human-relay/store"
)

//go:embed templates/*
var templateFS embed.FS

const defaultApprovalCooldown = 30 * time.Second

type Handler struct {
	store    *store.Store
	executor *executor.Executor
	audit    *audit.Logger
	tmpl     *template.Template
	sseClients map[chan []byte]struct{}
	sseMu      sync.Mutex
	lastApproval     time.Time
	cooldownMu       sync.Mutex
	approvalCooldown time.Duration
	turboCooldown    time.Duration
	turboExpiry      time.Time
}

type HandlerOption func(*Handler)

func WithCooldown(d time.Duration) HandlerOption {
	return func(h *Handler) {
		h.approvalCooldown = d
	}
}

func NewHandler(s *store.Store, exec *executor.Executor, al *audit.Logger, opts ...HandlerOption) *Handler {
	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))
	h := &Handler{
		store:            s,
		executor:         exec,
		audit:            al,
		tmpl:             tmpl,
		sseClients:       make(map[chan []byte]struct{}),
		approvalCooldown: defaultApprovalCooldown,
	}
	for _, opt := range opts {
		opt(h)
	}
	// Watch for new requests and broadcast to SSE clients
	go h.watchRequests()
	return h
}

func (h *Handler) activeCooldown() time.Duration {
	if !h.turboExpiry.IsZero() && time.Now().Before(h.turboExpiry) {
		return h.turboCooldown
	}
	return h.approvalCooldown
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", h.handleDashboard)
	mux.HandleFunc("/api/requests", h.handleListRequests)
	mux.HandleFunc("/api/requests/", h.handleRequestAction)
	mux.HandleFunc("/api/turbocharge", h.handleTurbocharge)
	mux.HandleFunc("/events", h.handleSSE)
}

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.tmpl.ExecuteTemplate(w, "index.html", nil)
}

func (h *Handler) handleListRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status := store.Status(r.URL.Query().Get("status"))
	requests := h.store.List(status)
	if requests == nil {
		requests = []*store.Request{}
	}
	// Tell the frontend how much cooldown remains (0 if none)
	h.cooldownMu.Lock()
	cd := h.activeCooldown()
	remaining := cd - time.Since(h.lastApproval)
	turboActive := !h.turboExpiry.IsZero() && time.Now().Before(h.turboExpiry)
	turboRemaining := time.Duration(0)
	if turboActive {
		turboRemaining = time.Until(h.turboExpiry)
	}
	h.cooldownMu.Unlock()
	if remaining < 0 {
		remaining = 0
	}
	w.Header().Set("X-Cooldown-Remaining-Ms", fmt.Sprintf("%d", remaining.Milliseconds()))
	w.Header().Set("X-Cooldown-Duration-Ms", fmt.Sprintf("%d", cd.Milliseconds()))
	if turboActive {
		w.Header().Set("X-Turbo-Active", "true")
		w.Header().Set("X-Turbo-Remaining-Ms", fmt.Sprintf("%d", turboRemaining.Milliseconds()))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(requests)
}

func (h *Handler) handleRequestAction(w http.ResponseWriter, r *http.Request) {
	// Parse /api/requests/{id}/{action}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/requests/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	action := parts[1]

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req := h.store.Get(id)
	if req == nil {
		http.Error(w, "request not found", http.StatusNotFound)
		return
	}
	if req.Status != store.StatusPending {
		http.Error(w, fmt.Sprintf("request is already %s", req.Status), http.StatusConflict)
		return
	}

	switch action {
	case "approve":
		h.cooldownMu.Lock()
		cd := h.activeCooldown()
		elapsed := time.Since(h.lastApproval)
		if elapsed < cd {
			remaining := cd - elapsed
			h.cooldownMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(remaining.Seconds())+1))
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":        "cooldown active",
				"remaining_ms": remaining.Milliseconds(),
			})
			return
		}
		h.lastApproval = time.Now()
		h.cooldownMu.Unlock()

		h.store.SetStatus(id, store.StatusApproved)
		log.Printf("request %s approved, executing: %s %v", id, req.Command, req.Args)
		h.audit.Log("request_approved", id, map[string]interface{}{
			"command": req.Command,
			"args":    req.Args,
		})
		h.broadcastEvent("update", id)

		// Execute in background
		go func() {
			h.store.SetStatus(id, store.StatusRunning)
			h.audit.Log("execution_started", id, nil)
			h.broadcastEvent("update", id)
			result := h.executor.Execute(req)
			status := store.StatusComplete
			if result.ExitCode != 0 {
				status = store.StatusError
			}
			h.store.SetResult(id, result, status)
			log.Printf("request %s completed with exit code %d", id, result.ExitCode)
			h.audit.Log("execution_completed", id, map[string]interface{}{
				"exit_code": result.ExitCode,
				"status":    string(status),
				"stdout":    audit.Truncate(result.Stdout),
				"stderr":    audit.Truncate(result.Stderr),
			})
			h.broadcastEvent("update", id)
		}()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "approved"})

	case "deny":
		var body struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Reason == "" {
			body.Reason = "denied by operator"
		}
		h.store.Deny(id, body.Reason)
		log.Printf("request %s denied: %s", id, body.Reason)
		h.audit.Log("request_denied", id, map[string]interface{}{
			"command":     req.Command,
			"args":        req.Args,
			"deny_reason": body.Reason,
		})
		h.broadcastEvent("update", id)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "denied"})

	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleTurbocharge(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.cooldownMu.Lock()
		active := !h.turboExpiry.IsZero() && time.Now().Before(h.turboExpiry)
		remaining := time.Duration(0)
		turboCd := h.turboCooldown
		if active {
			remaining = time.Until(h.turboExpiry)
		}
		h.cooldownMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active":           active,
			"remaining_ms":     remaining.Milliseconds(),
			"cooldown_seconds": int(turboCd.Seconds()),
		})

	case http.MethodPost:
		var body struct {
			DurationMinutes int `json:"duration_minutes"`
			CooldownSeconds int `json:"cooldown_seconds"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.DurationMinutes <= 0 {
			body.DurationMinutes = 5
		}
		if body.DurationMinutes > 30 {
			body.DurationMinutes = 30
		}
		if body.CooldownSeconds <= 0 {
			body.CooldownSeconds = 3
		}
		h.cooldownMu.Lock()
		h.turboCooldown = time.Duration(body.CooldownSeconds) * time.Second
		h.turboExpiry = time.Now().Add(time.Duration(body.DurationMinutes) * time.Minute)
		h.cooldownMu.Unlock()
		log.Printf("turbocharge activated: %ds cooldown for %d minutes", body.CooldownSeconds, body.DurationMinutes)
		h.audit.Log("turbocharge_on", "", map[string]interface{}{
			"cooldown_seconds":  body.CooldownSeconds,
			"duration_minutes":  body.DurationMinutes,
		})
		h.broadcastEvent("turbo", "on")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active":           true,
			"remaining_ms":     (time.Duration(body.DurationMinutes) * time.Minute).Milliseconds(),
			"cooldown_seconds": body.CooldownSeconds,
		})

	case http.MethodDelete:
		h.cooldownMu.Lock()
		h.turboExpiry = time.Time{}
		h.cooldownMu.Unlock()
		log.Printf("turbocharge deactivated")
		h.audit.Log("turbocharge_off", "", nil)
		h.broadcastEvent("turbo", "off")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "deactivated"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := make(chan []byte, 50)
	h.sseMu.Lock()
	h.sseClients[ch] = struct{}{}
	h.sseMu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			h.sseMu.Lock()
			delete(h.sseClients, ch)
			h.sseMu.Unlock()
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (h *Handler) broadcastEvent(eventType, requestID string) {
	data, _ := json.Marshal(map[string]string{
		"type":       eventType,
		"request_id": requestID,
	})
	h.sseMu.Lock()
	for ch := range h.sseClients {
		select {
		case ch <- data:
		default:
		}
	}
	h.sseMu.Unlock()
}

func (h *Handler) watchRequests() {
	sub := h.store.Subscribe()
	for id := range sub {
		h.broadcastEvent("new", id)
	}
}
