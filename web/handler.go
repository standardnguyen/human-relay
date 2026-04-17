package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"git.ekaterina.net/administrator/human-relay/audit"
	"git.ekaterina.net/administrator/human-relay/executor"
	"git.ekaterina.net/administrator/human-relay/store"
	"git.ekaterina.net/administrator/human-relay/whitelist"
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
	whitelist        *whitelist.Whitelist
	scriptsDir       string
}

type HandlerOption func(*Handler)

func WithCooldown(d time.Duration) HandlerOption {
	return func(h *Handler) {
		h.approvalCooldown = d
	}
}

func WithWhitelist(wl *whitelist.Whitelist) HandlerOption {
	return func(h *Handler) {
		h.whitelist = wl
	}
}

func WithScriptsDir(dir string) HandlerOption {
	return func(h *Handler) {
		h.scriptsDir = dir
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
		scriptsDir:       "/scripts",
	}
	for _, opt := range opts {
		opt(h)
	}
	// Watch for new requests and broadcast to SSE clients
	go h.watchRequests()
	// Watch for status updates (withdraw, etc) and broadcast
	go h.watchUpdates()
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
	mux.HandleFunc("/api/whitelist", h.handleWhitelist)
	mux.HandleFunc("/api/whitelist/remove", h.handleWhitelistRemove)
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
	sortRequests(requests, status)
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
		switch req.Type {
		case "http":
			log.Printf("request %s approved, executing: %s %s", id, req.HTTPMethod, req.HTTPURL)
			h.audit.Log("request_approved", id, map[string]interface{}{
				"type":   "http",
				"method": req.HTTPMethod,
				"url":    req.HTTPURL,
			})
		case "script":
			log.Printf("request %s approved, executing script: %s", id, req.ScriptName)
			h.audit.Log("request_approved", id, map[string]interface{}{
				"type":   "script",
				"script": req.ScriptName,
			})
		case "script_create":
			log.Printf("request %s approved, creating script: %s", id, req.ScriptName)
			h.audit.Log("request_approved", id, map[string]interface{}{
				"type":   "script_create",
				"script": req.ScriptName,
			})
		default:
			log.Printf("request %s approved, executing: %s %v", id, req.Command, req.Args)
			h.audit.Log("request_approved", id, map[string]interface{}{
				"command": req.Command,
				"args":    req.Args,
			})
		}
		h.broadcastEvent("update", id)

		// Execute in background
		go h.executeRequest(req)

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
	// Send an initial comment so EventSource fires onopen in all browsers
	fmt.Fprintf(w, ": connected\n\n")
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

// watchUpdates listens for status-change events from the store (currently:
// Withdraw) and broadcasts them to SSE clients so the dashboard refreshes.
// Approve/deny are broadcast directly from handleRequestAction since those
// mutations originate in the web handler.
func (h *Handler) watchUpdates() {
	sub := h.store.SubscribeUpdates()
	for id := range sub {
		h.broadcastEvent("update", id)
	}
}

func (h *Handler) watchRequests() {
	sub := h.store.Subscribe()
	for id := range sub {
		h.broadcastEvent("new", id)
		if h.whitelist != nil {
			req := h.store.Get(id)
			wlCommand, wlArgs := req.Command, req.Args
			if req.Type == "http" {
				wlCommand = req.HTTPMethod
				wlArgs = []string{req.HTTPURL}
			} else if req.Type == "script" {
				wlCommand = "run_script"
				wlArgs = []string{req.ScriptName}
			} else if req.Type == "script_create" {
				wlCommand = "create_script"
				wlArgs = []string{req.ScriptName}
			}
			if req != nil && req.Status == store.StatusPending && h.whitelist.Match(wlCommand, wlArgs) {
				h.autoApprove(req)
			}
		}
	}
}

func (h *Handler) autoApprove(req *store.Request) {
	h.store.SetStatus(req.ID, store.StatusApproved)
	log.Printf("request %s auto-approved (whitelist): %s %v", req.ID, req.Command, req.Args)
	h.audit.Log("request_auto_approved", req.ID, map[string]interface{}{
		"command": req.Command,
		"args":    req.Args,
	})
	h.broadcastEvent("update", req.ID)

	go h.executeRequest(req)
}

// executeRequest dispatches to the appropriate executor based on request type.
func (h *Handler) executeRequest(req *store.Request) {
	h.store.SetStatus(req.ID, store.StatusRunning)
	h.audit.Log("execution_started", req.ID, nil)
	h.broadcastEvent("update", req.ID)

	var result *store.Result
	switch req.Type {
	case "http":
		result = h.executor.ExecuteHTTP(req)
	case "script":
		result = h.executor.ExecuteScript(req)
	case "script_create":
		result = h.executor.ExecuteScriptCreate(req, h.scriptsDir)
	default:
		result = h.executor.Execute(req)
	}

	status := store.StatusComplete
	if result.ExitCode != 0 {
		status = store.StatusError
	}
	h.store.SetResult(req.ID, result, status)
	log.Printf("request %s completed with exit code %d", req.ID, result.ExitCode)
	h.audit.Log("execution_completed", req.ID, map[string]interface{}{
		"exit_code": result.ExitCode,
		"status":    string(status),
		"stdout":    audit.Truncate(result.Stdout),
		"stderr":    audit.Truncate(result.Stderr),
	})
	h.broadcastEvent("update", req.ID)
}

func (h *Handler) handleWhitelist(w http.ResponseWriter, r *http.Request) {
	if h.whitelist == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		rules := h.whitelist.Rules()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rules)

	case http.MethodPost:
		var body struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Command == "" {
			http.Error(w, "command is required", http.StatusBadRequest)
			return
		}
		h.whitelist.Add(body.Command, body.Args)
		if err := h.whitelist.Save(); err != nil {
			log.Printf("whitelist save error: %v", err)
		}
		h.audit.Log("whitelist_add", "", map[string]interface{}{
			"command": body.Command,
			"args":    body.Args,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "added"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleWhitelistRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.whitelist == nil {
		http.Error(w, "whitelist not configured", http.StatusNotFound)
		return
	}

	var body struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Command == "" {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}

	removed := h.whitelist.Remove(body.Command, body.Args)
	if !removed {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	if err := h.whitelist.Save(); err != nil {
		log.Printf("whitelist save error: %v", err)
	}
	h.audit.Log("whitelist_remove", "", map[string]interface{}{
		"command": body.Command,
		"args":    body.Args,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "removed"})
}

// sortRequests sorts the request list for the web dashboard.
// Complete/denied/error: newest first. Pending/running: oldest first.
// Unfiltered (all): pending oldest-first at top, non-pending newest-first after.
func sortRequests(requests []*store.Request, filter store.Status) {
	newestFirst := filter == store.StatusComplete || filter == store.StatusDenied || filter == store.StatusError
	sort.SliceStable(requests, func(i, j int) bool {
		a, b := requests[i], requests[j]
		if filter == "" {
			// All view: pending items first (oldest-first), then non-pending (newest-first)
			aPending := a.Status == store.StatusPending
			bPending := b.Status == store.StatusPending
			if aPending != bPending {
				return aPending
			}
			if aPending {
				return a.CreatedAt.Before(b.CreatedAt)
			}
			return b.CreatedAt.Before(a.CreatedAt)
		}
		if newestFirst {
			return b.CreatedAt.Before(a.CreatedAt)
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
}
