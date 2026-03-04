package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.ekaterina.net/administrator/human-relay/containers"
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

	dataDir := envString("MHR_DATA_DIR", "/opt/human-relay/data")
	hostIP := envString("MHR_HOST_IP", "192.168.10.50")

	var allowedDirs []string
	if dirs := os.Getenv("MHR_ALLOWED_DIRS"); dirs != "" {
		for _, d := range strings.Split(dirs, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				allowedDirs = append(allowedDirs, d)
			}
		}
	}

	// Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	s := store.New()
	exec := executor.New(executor.Config{
		DefaultTimeout: defaultTimeout,
		MaxTimeout:     maxTimeout,
		AllowedDirs:    allowedDirs,
	})

	// Container registry (JSON file)
	registryPath := filepath.Join(dataDir, "containers.json")
	containerStore, err := containers.NewStore(registryPath)
	if err != nil {
		log.Fatalf("init container store: %v", err)
	}
	defer containerStore.Close()
	log.Printf("Container registry: %s", registryPath)

	// MCP server (SSE transport)
	toolHandler := mcp.NewToolHandler(s, containerStore, hostIP)
	mcpServer := mcp.NewServer(toolHandler)

	// Web dashboard
	var webOpts []web.HandlerOption
	if cd := envInt("MHR_APPROVAL_COOLDOWN", 0); cd > 0 {
		webOpts = append(webOpts, web.WithCooldown(time.Duration(cd)*time.Second))
	}
	webHandler := web.NewHandler(s, exec, webOpts...)
	webMux := http.NewServeMux()
	webHandler.RegisterRoutes(webMux)

	// The /events SSE endpoint is unauthenticated. This is a deliberate trade-off:
	// EventSource cannot set custom headers (no Authorization header possible).
	// Risk: an attacker on the local network could subscribe and observe command
	// metadata (names, reasons, statuses) in real time. Accepted because:
	//   1. The server is only exposed on private/tailnet networks.
	//   2. /events is read-only — no mutations are possible through it.
	//   3. All approve/deny/list API endpoints still require Bearer auth.
	//   4. Adding cookie/query-param auth would add complexity with minimal gain.
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
	log.Printf("  Host IP:    %s", hostIP)
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

func envString(key string, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
