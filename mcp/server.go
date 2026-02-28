package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
)

type Server struct {
	handler *ToolHandler
	clients map[string]chan []byte // sessionID -> message channel
	mu      sync.Mutex
	nextID  int
}

func NewServer(handler *ToolHandler) *Server {
	return &Server{
		handler: handler,
		clients: make(map[string]chan []byte),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/sse" && r.Method == http.MethodGet:
		s.handleSSE(w, r)
	case strings.HasPrefix(r.URL.Path, "/message") && r.Method == http.MethodPost:
		s.handleMessage(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.nextID++
	sessionID := fmt.Sprintf("session-%d", s.nextID)
	ch := make(chan []byte, 100)
	s.clients[sessionID] = ch
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send the endpoint event so the client knows where to POST messages
	fmt.Fprintf(w, "event: endpoint\ndata: /message?sessionId=%s\n\n", sessionID)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			delete(s.clients, sessionID)
			s.mu.Unlock()
			return
		case msg := <-ch:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	s.mu.Lock()
	ch, ok := s.clients[sessionID]
	s.mu.Unlock()

	if !ok {
		http.Error(w, "unknown session", http.StatusBadRequest)
		return
	}

	// Read the JSON-RPC request. Handle both single requests and batched.
	// The MCP spec sends one request at a time over the message endpoint.
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var body []byte
	for scanner.Scan() {
		body = append(body, scanner.Bytes()...)
	}
	if len(body) == 0 {
		body, _ = readAll(r.Body)
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	resp := s.dispatch(req)
	if resp == nil {
		// Notification — no response needed
		w.WriteHeader(http.StatusAccepted)
		return
	}

	data, _ := json.Marshal(resp)
	select {
	case ch <- data:
	default:
		log.Printf("warning: message channel full for session %s", sessionID)
	}

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) dispatch(req JSONRPCRequest) *JSONRPCResponse {
	switch req.Method {
	case "initialize":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: InitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities: Capabilities{
					Tools: &ToolsCapability{},
				},
				ServerInfo: ServerInfo{
					Name:    "human-relay",
					Version: "0.1.0",
				},
			},
		}

	case "notifications/initialized":
		// Notification, no response
		return nil

	case "tools/list":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  ToolsListResult{Tools: ToolDefinitions},
		}

	case "tools/call":
		params, _ := json.Marshal(req.Params)
		var callParams CallToolParams
		json.Unmarshal(params, &callParams)

		result := s.handler.Handle(callParams.Name, callParams.Arguments)
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}

	case "ping":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]interface{}{},
		}

	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &RPCError{
				Code:    -32601,
				Message: fmt.Sprintf("method not found: %s", req.Method),
			},
		}
	}
}

func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			return buf, err
		}
	}
}
