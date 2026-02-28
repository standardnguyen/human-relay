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

	"git.ekaterina.net/administrator/human-relay/executor"
	"git.ekaterina.net/administrator/human-relay/store"
)

//go:embed templates/*
var templateFS embed.FS

type Handler struct {
	store    *store.Store
	executor *executor.Executor
	tmpl     *template.Template
	sseClients map[chan []byte]struct{}
	sseMu      sync.Mutex
}

func NewHandler(s *store.Store, exec *executor.Executor) *Handler {
	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))
	h := &Handler{
		store:      s,
		executor:   exec,
		tmpl:       tmpl,
		sseClients: make(map[chan []byte]struct{}),
	}
	// Watch for new requests and broadcast to SSE clients
	go h.watchRequests()
	return h
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", h.handleDashboard)
	mux.HandleFunc("/api/requests", h.handleListRequests)
	mux.HandleFunc("/api/requests/", h.handleRequestAction)
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
		h.store.SetStatus(id, store.StatusApproved)
		log.Printf("request %s approved, executing: %s %v", id, req.Command, req.Args)
		h.broadcastEvent("update", id)

		// Execute in background
		go func() {
			h.store.SetStatus(id, store.StatusRunning)
			h.broadcastEvent("update", id)
			result := h.executor.Execute(req)
			status := store.StatusComplete
			if result.ExitCode != 0 {
				status = store.StatusError
			}
			h.store.SetResult(id, result, status)
			log.Printf("request %s completed with exit code %d", id, result.ExitCode)
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
		h.broadcastEvent("update", id)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "denied"})

	default:
		http.NotFound(w, r)
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
