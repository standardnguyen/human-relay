package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/standardnguyen/human-relay/audit"
	"github.com/standardnguyen/human-relay/containers"
	"github.com/standardnguyen/human-relay/executor"
	"github.com/standardnguyen/human-relay/machines"
	"github.com/standardnguyen/human-relay/permissions"
	"github.com/standardnguyen/human-relay/store"
	"github.com/standardnguyen/human-relay/whitelist"
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
	permissions      *permissions.Permissions
	scriptsDir       string
	containerStore   *containers.Store
	machineStore     *machines.Store
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

func WithPermissions(p *permissions.Permissions) HandlerOption {
	return func(h *Handler) {
		h.permissions = p
	}
}

// WithRegistries wires the container and machine registries into the handler.
// registry_op requests (register_container, delete_container, register_machine,
// delete_machine) are queued by the MCP tools and applied here after approval,
// so without this option those requests fail with "registry not configured".
func WithRegistries(cs *containers.Store, ms *machines.Store) HandlerOption {
	return func(h *Handler) {
		h.containerStore = cs
		h.machineStore = ms
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
	mux.HandleFunc("/chat", h.handleChat)
	mux.HandleFunc("/api/requests", h.handleListRequests)
	mux.HandleFunc("/api/requests/", h.handleRequestAction)
	mux.HandleFunc("/api/turbocharge", h.handleTurbocharge)
	mux.HandleFunc("/api/whitelist", h.handleWhitelist)
	mux.HandleFunc("/api/whitelist/remove", h.handleWhitelistRemove)
	mux.HandleFunc("/api/permission/check", h.handlePermissionCheck)
	mux.HandleFunc("/api/permission/check/", h.handlePermissionStatus)
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

// handleChat serves the chat-shaped view of the signal-lane approval queue —
// the same requests as the dashboard, rendered as conversation bubbles.
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.tmpl.ExecuteTemplate(w, "chat.html", nil)
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

	if action == "release" {
		if !req.OutputGated {
			http.Error(w, "output is not gated", http.StatusConflict)
			return
		}
		h.store.ReleaseOutput(id)
		log.Printf("request %s output released", id)
		h.audit.Log("output_released", id, nil)
		h.broadcastEvent("update", id)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "released"})
		return
	}

	if req.Status != store.StatusPending {
		http.Error(w, fmt.Sprintf("request is already %s", req.Status), http.StatusConflict)
		return
	}

	switch action {
	case "approve", "approve-gated":
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

		// Atomic approve: the pending check above is only a fast, friendly
		// rejection for an obviously-decided request. This is the authoritative
		// guard -- it holds the store lock across lookup, pending check and
		// mutation, so two concurrent approvals of the same request can never
		// both execute it.
		ok, approved := h.store.Approve(id, action == "approve-gated")
		if !ok {
			current := store.Status("decided")
			if cur := h.store.Get(id); cur != nil {
				current = cur.Status
			}
			log.Printf("request %s approve raced a concurrent decision (now %s), ignoring", id, current)
			http.Error(w, fmt.Sprintf("request is already %s", current), http.StatusConflict)
			return
		}
		req = approved
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
		case "script_create_then_run":
			log.Printf("request %s approved, create+run script: %s", id, req.ScriptName)
			h.audit.Log("request_approved", id, map[string]interface{}{
				"type":   "script_create_then_run",
				"script": req.ScriptName,
			})
		case "registry_op":
			log.Printf("request %s approved, applying registry op: %s", id, req.RegistryOp)
			h.audit.Log("request_approved", id, map[string]interface{}{
				"type":          "registry_op",
				"registry_op":   req.RegistryOp,
				"registry_args": req.RegistryArgs,
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

// whitelistKey maps a request to the (command, args) pair the whitelist is
// keyed on. Both sides of the match go through it — watchRequests when
// deciding whether an incoming request auto-approves, and handleWhitelist when
// recording the rule an operator clicked — so the two cannot drift apart.
//
// Script creates include a hash of the script body in the key. Keying them on
// the name alone made "whitelist this create_script" a standing grant to run
// ANY future content submitted under that name with no human review.
func whitelistKey(req *store.Request) (string, []string) {
	switch req.Type {
	case "http":
		return req.HTTPMethod, []string{req.HTTPURL}
	case "script":
		return "run_script", []string{req.ScriptName}
	case "script_create":
		return "create_script", []string{req.ScriptName, store.StdinDigest(req.Stdin)}
	case "script_create_then_run":
		return "create_then_run", []string{req.ScriptName, store.StdinDigest(req.Stdin)}
	}
	return req.Command, req.Args
}

func (h *Handler) watchRequests() {
	sub := h.store.Subscribe()
	for id := range sub {
		h.broadcastEvent("new", id)
		if h.whitelist != nil {
			req := h.store.Get(id)
			if req == nil {
				continue
			}
			wlCommand, wlArgs := whitelistKey(req)
			if rule, ok := h.whitelist.MatchRule(wlCommand, wlArgs); ok && req.Status == store.StatusPending {
				h.autoApprove(req, rule.GateOutput)
			}
		}
	}
}

func (h *Handler) autoApprove(req *store.Request, gateOutput bool) {
	// Same atomic guard as the manual approve path: the pending check in
	// watchRequests is a separate read, so without this a whitelist
	// auto-approval could race an operator click and execute the request twice.
	// gateOutput means "whitelist but gate outputs": execution is auto-approved,
	// but the result stays output_gated until the human releases it.
	ok, approved := h.store.Approve(req.ID, gateOutput)
	if !ok {
		log.Printf("request %s auto-approve raced a concurrent decision, ignoring", req.ID)
		return
	}
	req = approved
	log.Printf("request %s auto-approved (whitelist, gated=%v): %s %v", req.ID, gateOutput, req.Command, req.Args)
	h.audit.Log("request_auto_approved", req.ID, map[string]interface{}{
		"command":     req.Command,
		"args":        req.Args,
		"gate_output": gateOutput,
	})
	h.broadcastEvent("update", req.ID)

	go h.executeRequest(req)
}

// executeRequest dispatches to the appropriate executor based on request type.
func (h *Handler) executeRequest(req *store.Request) {
	// Permission-check requests carry no work — approval IS the verdict.
	// Mark complete with a zero-exit result so polling clients see done.
	if req.Type == "permission" {
		result := &store.Result{ExitCode: 0, Stdout: "approved"}
		h.store.SetResult(req.ID, result, store.StatusComplete)
		h.audit.Log("permission_approved", req.ID, map[string]interface{}{
			"display": req.DisplayCommand,
		})
		h.broadcastEvent("update", req.ID)
		return
	}

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
	case "script_create_then_run":
		result = h.executor.ExecuteScriptCreateThenRun(req, h.scriptsDir)
	case "registry_op":
		result = executeRegistryOp(req, h.containerStore, h.machineStore)
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
	// Gating protects content; an empty result has none to screen. Auto-release
	// so gated-whitelist polls that find nothing don't queue no-op Release clicks.
	if cur := h.store.Get(req.ID); cur != nil && cur.OutputGated && result.Stdout == "" && result.Stderr == "" {
		h.store.ReleaseOutput(req.ID)
		h.audit.Log("output_auto_released_empty", req.ID, nil)
	}
	h.broadcastEvent("update", req.ID)
}

// executeRegistryOp applies an approved container/machine registry mutation.
// The MCP tools (register_container, delete_container, register_machine,
// delete_machine) validate their arguments and queue the request; the registry
// is only touched here, after a human approved it in the dashboard. Success is
// a zero-exit result whose stdout is the JSON the tool used to return
// synchronously; failure is exit 1 with the error on stderr.
func executeRegistryOp(req *store.Request, cs *containers.Store, ms *machines.Store) *store.Result {
	args := req.RegistryArgs
	switch req.RegistryOp {
	case "register_container":
		if cs == nil {
			return registryFailure("container registry not configured")
		}
		ctid, err := strconv.Atoi(args["ctid"])
		if err != nil {
			return registryFailure(fmt.Sprintf("invalid ctid %q: %v", args["ctid"], err))
		}
		c, err := cs.Register(ctid, args["ip"], args["hostname"], args["has_relay_ssh"] == "true", args["ssh_user"])
		if err != nil {
			return registryFailure(fmt.Sprintf("failed to register container: %v", err))
		}
		return registrySuccess(c)

	case "delete_container":
		if cs == nil {
			return registryFailure("container registry not configured")
		}
		ctid, err := strconv.Atoi(args["ctid"])
		if err != nil {
			return registryFailure(fmt.Sprintf("invalid ctid %q: %v", args["ctid"], err))
		}
		if err := cs.Delete(ctid); err != nil {
			return registryFailure(fmt.Sprintf("failed to delete container: %v", err))
		}
		return registrySuccess(map[string]interface{}{"ctid": ctid, "deleted": true})

	case "register_machine":
		if ms == nil {
			return registryFailure("machine registry not configured")
		}
		m, err := ms.Register(args["name"], args["host"], args["ssh_user"], args["shell"], args["identity_file"])
		if err != nil {
			return registryFailure(fmt.Sprintf("failed to register machine: %v", err))
		}
		return registrySuccess(m)

	case "delete_machine":
		if ms == nil {
			return registryFailure("machine registry not configured")
		}
		if err := ms.Delete(args["name"]); err != nil {
			return registryFailure(fmt.Sprintf("failed to delete machine: %v", err))
		}
		return registrySuccess(map[string]interface{}{"name": args["name"], "deleted": true})

	default:
		return registryFailure(fmt.Sprintf("unknown registry op %q", req.RegistryOp))
	}
}

func registrySuccess(v interface{}) *store.Result {
	data, err := json.Marshal(v)
	if err != nil {
		return registryFailure(fmt.Sprintf("marshal registry result: %v", err))
	}
	return &store.Result{ExitCode: 0, Stdout: string(data)}
}

func registryFailure(msg string) *store.Result {
	return &store.Result{ExitCode: 1, Stderr: msg}
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
			// RequestID names the request being whitelisted. When present the
			// rule's key is derived server-side from the stored request via
			// whitelistKey — the same function the auto-approve path matches
			// with — instead of being taken from the client. That is what lets
			// script creates key on a body hash the browser never sees.
			RequestID  string   `json:"request_id"`
			Command    string   `json:"command"`
			Args       []string `json:"args"`
			GateOutput bool     `json:"gate_output"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		command, wlArgs := body.Command, body.Args
		if body.RequestID != "" {
			req := h.store.Get(body.RequestID)
			if req == nil {
				http.Error(w, "request not found", http.StatusNotFound)
				return
			}
			command, wlArgs = whitelistKey(req)
		}
		if command == "" {
			http.Error(w, "command is required", http.StatusBadRequest)
			return
		}
		h.whitelist.Add(command, wlArgs, body.GateOutput)
		if err := h.whitelist.Save(); err != nil {
			log.Printf("whitelist save error: %v", err)
		}
		h.audit.Log("whitelist_add", body.RequestID, map[string]interface{}{
			"command":     command,
			"args":        wlArgs,
			"gate_output": body.GateOutput,
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

// handlePermissionCheck is POST /api/permission/check.
// Body: {"tool":"Bash","input":{"command":"ls -la"},"reason":"list cwd"}
// Response (allow/deny): {"verdict":"allow|deny","rule_id":"...","reason":"..."}
// Response (ask):       {"verdict":"ask","request_id":"abc","rule_id":"..."}
// On 'ask' the caller polls GET /api/permission/check/{request_id} until the
// human approves or denies in the dashboard.
func (h *Handler) handlePermissionCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.permissions == nil {
		http.Error(w, "permissions not configured", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		Tool   string         `json:"tool"`
		Input  map[string]any `json:"input"`
		Reason string         `json:"reason"`
		Client string         `json:"client"` // cosmetic: "pi", "rolandcode", etc. surfaced in audit + queue UI
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if body.Tool == "" {
		http.Error(w, "tool required", http.StatusBadRequest)
		return
	}
	if body.Input == nil {
		body.Input = map[string]any{}
	}

	d := h.permissions.Check(body.Tool, body.Input)

	resp := map[string]any{
		"verdict": string(d.Verdict),
		"rule_id": d.RuleID,
	}
	if d.Reason != "" {
		resp["reason"] = d.Reason
	}

	h.audit.Log("permission_check", "", map[string]interface{}{
		"tool":    body.Tool,
		"input":   body.Input,
		"reason":  body.Reason,
		"client":  body.Client,
		"verdict": string(d.Verdict),
		"rule_id": d.RuleID,
	})

	if d.Verdict == permissions.VerdictAsk {
		display := formatPermissionDisplay(body.Tool, body.Input)
		if body.Client != "" {
			display = "[" + body.Client + "] " + display
		}
		req := h.store.AddPermission(display, body.Reason, 300)
		resp["request_id"] = req.ID
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handlePermissionStatus is GET /api/permission/check/{id}.
// Returns the current status of an ask-routed permission request.
// Response: {"status":"pending|approved|denied|...", "verdict":"ask|allow|deny", "reason":"..."}
func (h *Handler) handlePermissionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/permission/check/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	req := h.store.Get(id)
	if req == nil {
		http.NotFound(w, r)
		return
	}
	if req.Type != "permission" {
		http.Error(w, "not a permission request", http.StatusBadRequest)
		return
	}

	resp := map[string]any{
		"status": string(req.Status),
	}
	switch req.Status {
	case store.StatusPending, store.StatusRunning:
		resp["verdict"] = "ask"
	case store.StatusApproved, store.StatusComplete:
		resp["verdict"] = "allow"
	case store.StatusDenied:
		resp["verdict"] = "deny"
		resp["reason"] = req.DenyReason
	case store.StatusWithdrawn, store.StatusTimeout, store.StatusError:
		resp["verdict"] = "deny"
		resp["reason"] = string(req.Status)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func formatPermissionDisplay(tool string, input map[string]any) string {
	switch strings.ToLower(tool) {
	case "bash":
		if cmd, ok := input["command"].(string); ok {
			return fmt.Sprintf("Bash: %s", cmd)
		}
	case "read", "write", "edit":
		for _, k := range []string{"file_path", "path"} {
			if p, ok := input[k].(string); ok {
				return fmt.Sprintf("%s: %s", tool, p)
			}
		}
	}
	b, _ := json.Marshal(input)
	return fmt.Sprintf("%s: %s", tool, string(b))
}
