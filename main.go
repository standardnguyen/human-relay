package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"git.ekaterina.net/administrator/human-relay/executor"
	"git.ekaterina.net/administrator/human-relay/mcp"
	"git.ekaterina.net/administrator/human-relay/store"
	"git.ekaterina.net/administrator/human-relay/web"
)

func main() {
	authToken := os.Getenv("MHR_AUTH_TOKEN")
	if authToken == "" {
		log.Fatal("MHR_AUTH_TOKEN is required")
	}

	mcpPort := envInt("MHR_MCP_PORT", 8080)
	webPort := envInt("MHR_WEB_PORT", 8090)
	defaultTimeout := envInt("MHR_DEFAULT_TIMEOUT", 30)
	maxTimeout := envInt("MHR_MAX_TIMEOUT", 300)

	var allowedDirs []string
	if dirs := os.Getenv("MHR_ALLOWED_DIRS"); dirs != "" {
		for _, d := range strings.Split(dirs, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				allowedDirs = append(allowedDirs, d)
			}
		}
	}

	s := store.New()
	exec := executor.New(executor.Config{
		DefaultTimeout: defaultTimeout,
		MaxTimeout:     maxTimeout,
		AllowedDirs:    allowedDirs,
	})

	// MCP server (SSE transport)
	toolHandler := mcp.NewToolHandler(s)
	mcpServer := mcp.NewServer(toolHandler)

	// Web dashboard
	webHandler := web.NewHandler(s, exec)
	webMux := http.NewServeMux()
	webHandler.RegisterRoutes(webMux)

	// The /events SSE endpoint is unauthenticated (EventSource can't set headers).
	// All mutation/data endpoints require auth.
	authedMux := http.NewServeMux()
	authedMux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		webMux.ServeHTTP(w, r)
	})
	authedMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Dashboard page itself doesn't need auth (token entered client-side)
		if r.URL.Path == "/" && r.Method == http.MethodGet {
			webMux.ServeHTTP(w, r)
			return
		}
		// Everything else goes through auth + CSRF
		web.AuthMiddleware(authToken,
			web.CSRFMiddleware(webMux),
		).ServeHTTP(w, r)
	})

	log.Printf("Human Relay starting")
	log.Printf("  MCP server: :%d/sse", mcpPort)
	log.Printf("  Web UI:     :%d", webPort)
	if len(allowedDirs) > 0 {
		log.Printf("  Allowed dirs: %v", allowedDirs)
	}

	errCh := make(chan error, 2)

	go func() {
		errCh <- http.ListenAndServe(fmt.Sprintf(":%d", mcpPort), mcpServer)
	}()

	go func() {
		errCh <- http.ListenAndServe(fmt.Sprintf(":%d", webPort), authedMux)
	}()

	log.Fatal(<-errCh)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
