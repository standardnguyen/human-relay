package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/standardnguyen/human-relay/audit"
	"github.com/standardnguyen/human-relay/auth"
	"github.com/standardnguyen/human-relay/containers"
	"github.com/standardnguyen/human-relay/executor"
	"github.com/standardnguyen/human-relay/machines"
	"github.com/standardnguyen/human-relay/mcp"
	"github.com/standardnguyen/human-relay/permissions"
	"github.com/standardnguyen/human-relay/store"
	"github.com/standardnguyen/human-relay/web"
	"github.com/standardnguyen/human-relay/whitelist"
)

func main() {
	// Before anything else: a plaintext approver token must not stay in this
	// process's /proc/<pid>/environ, where every command it runs could read it.
	scrubPlaintextApproverToken()

	dataDir := envString("MHR_DATA_DIR", "/opt/human-relay/data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// Client-token management runs before the server starts, so it needs
	// neither a store nor an auth token. -client-add prints the minted token
	// once and exits.
	clientsPath := envString("MHR_CLIENTS_FILE", filepath.Join(dataDir, "clients.json"))
	if handled, code := handleClientCommand(os.Args[1:], clientsPath); handled {
		os.Exit(code)
	}

	authToken := os.Getenv("MHR_AUTH_TOKEN")
	if authToken == "" {
		log.Fatal("MHR_AUTH_TOKEN is required")
	}

	mcpPort := envInt("MHR_MCP_PORT", 8080)
	webPort := envInt("MHR_WEB_PORT", 8090)
	defaultTimeout := envInt("MHR_DEFAULT_TIMEOUT", 30)
	maxTimeout := envInt("MHR_MAX_TIMEOUT", 300)

	hostIP := envString("MHR_HOST_IP", "")

	var allowedDirs []string
	if dirs := os.Getenv("MHR_ALLOWED_DIRS"); dirs != "" {
		for _, d := range strings.Split(dirs, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				allowedDirs = append(allowedDirs, d)
			}
		}
	}

	// Client registry (per-client bearer tokens, JSON file)
	clientRegistry, err := auth.NewRegistry(clientsPath)
	if err != nil {
		log.Fatalf("init client registry: %v", err)
	}
	verifier := auth.NewVerifier(clientRegistry, authToken)
	log.Printf("Client registry: %s", clientsPath)

	// Approver token: the only credential that may approve, deny, release,
	// whitelist or turbocharge once set. The web port verifies it; the MCP
	// port never does, so no agent credential can double as it. Unset keeps
	// the legacy behaviour, where every authenticating token can decide.
	//
	// MHR_APPROVER_TOKEN_SHA256 (the token's hex SHA-256) is the better form:
	// the relay needs only the digest, so the token itself never has to exist
	// on the relay host. A plaintext token was already swapped for its digest
	// by scrubPlaintextApproverToken's re-exec, so the kernel's copy of the
	// initial environment never holds it. Either variable is also dropped from
	// Go's environment once read, so commands the relay runs do not inherit it.
	webVerifier := verifier
	approverDigest, approverSet := approverDigestFromEnv()
	os.Unsetenv("MHR_APPROVER_TOKEN")
	os.Unsetenv("MHR_APPROVER_TOKEN_SHA256")
	if approverSet {
		authDigest := sha256.Sum256([]byte(authToken))
		if subtle.ConstantTimeCompare(approverDigest[:], authDigest[:]) == 1 {
			log.Fatal("the approver token must differ from MHR_AUTH_TOKEN: agents hold the auth token, so sharing it would let them approve their own requests")
		}
		if name, ok := clientRegistry.MatchDigest(approverDigest); ok {
			log.Fatalf("the approver token must differ from every client token, but it matches client %q", name)
		}
		webVerifier = verifier.WithApproverDigest(approverDigest)
		log.Printf("Approver token: set; only it may approve, deny, release, whitelist or turbocharge")
	} else {
		log.Printf("WARNING: MHR_APPROVER_TOKEN is not set, so any token that authenticates, including the ones agents use on the MCP port, can approve, deny, release and whitelist requests. Set it to a token no agent holds.")
	}

	s := store.New()
	exec := executor.New(executor.Config{
		DefaultTimeout: defaultTimeout,
		MaxTimeout:     maxTimeout,
		AllowedDirs:    allowedDirs,
	})

	// Audit log (append-only JSONL)
	auditPath := filepath.Join(dataDir, "audit.log")
	auditLog, err := audit.NewLogger(auditPath)
	if err != nil {
		log.Fatalf("init audit log: %v", err)
	}
	defer auditLog.Close()
	log.Printf("Audit log: %s", auditPath)

	// Container registry (JSON file)
	registryPath := filepath.Join(dataDir, "containers.json")
	containerStore, err := containers.NewStore(registryPath)
	if err != nil {
		log.Fatalf("init container store: %v", err)
	}
	defer containerStore.Close()
	log.Printf("Container registry: %s", registryPath)

	// Machine registry (non-LXC SSH targets — Windows, bare metal, VMs, WSL)
	machinesPath := filepath.Join(dataDir, "machines.json")
	machineStore, err := machines.NewStore(machinesPath)
	if err != nil {
		log.Fatalf("init machine store: %v", err)
	}
	defer machineStore.Close()
	log.Printf("Machine registry: %s", machinesPath)

	// Whitelist (auto-approve matching commands)
	wlPath := envString("MHR_WHITELIST_FILE", filepath.Join(dataDir, "whitelist.json"))
	wl, err := whitelist.Load(wlPath)
	if err != nil {
		log.Fatalf("load whitelist: %v", err)
	}
	if rules := wl.Rules(); len(rules) > 0 {
		log.Printf("Whitelist: %d rules from %s", len(rules), wlPath)
	}

	// MCP server (SSE transport)
	toolHandler := mcp.NewToolHandler(s, containerStore, machineStore, hostIP, auditLog)
	if sshCfg := os.Getenv("MHR_SSH_CONFIG"); sshCfg != "" {
		toolHandler.SetSSHConfig(sshCfg)
		log.Printf("  SSH config: %s", sshCfg)
	}
	relayPubkey := envString("MHR_RELAY_PUBKEY_FILE", "/root/.ssh/id_ed25519.pub")
	toolHandler.SetRelayPubkeyFile(relayPubkey)
	log.Printf("  Relay pubkey: %s", relayPubkey)
	mcpServer := mcp.NewServer(toolHandler)

	// Web dashboard
	scriptsDir := envString("MHR_SCRIPTS_DIR", "/scripts")
	toolHandler.SetScriptsDir(scriptsDir)

	// Permissions ruleset (tool-call gating for pi-relay-gate and friends)
	permPath := envString("MHR_PERMISSIONS_FILE", filepath.Join(dataDir, "permissions.json"))
	perms, err := permissions.Load(permPath)
	if err != nil {
		log.Fatalf("load permissions: %v", err)
	}
	rules := perms.Rules()
	log.Printf("Permissions: %d allow, %d deny, %d ask from %s", len(rules.Allow), len(rules.Deny), len(rules.Ask), permPath)

	cd := envInt("MHR_APPROVAL_COOLDOWN", 30)
	webHandler := web.NewHandler(s, exec, auditLog, web.WithCooldown(time.Duration(cd)*time.Second), web.WithWhitelist(wl), web.WithScriptsDir(scriptsDir), web.WithPermissions(perms), web.WithRegistries(containerStore, machineStore), web.WithApproverRequired(approverSet))
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
		// The dashboard and chat pages themselves don't need auth (the token is
		// entered client-side and used only for the API/action calls they make).
		if r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/chat") {
			webMux.ServeHTTP(w, r)
			return
		}
		// Everything else goes through auth + CSRF
		web.AuthMiddleware(webVerifier,
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

	// The MCP port exposes the full tool surface (request_command, write_file,
	// exec_container, ...), so it requires the same bearer token as the web API.
	// No CSRF middleware here: nothing on this port is browser-originated.
	go func() {
		errCh <- http.ListenAndServe(fmt.Sprintf(":%d", mcpPort), web.AuthMiddleware(verifier, mcpServer))
	}()

	go func() {
		errCh <- http.ListenAndServe(fmt.Sprintf(":%d", webPort), authedMux)
	}()

	log.Fatal(<-errCh)
}

// handleClientCommand implements the -client-add / -client-list /
// -client-revoke subcommands. It returns (handled, exitCode); when handled is
// true the caller exits with exitCode. The token minted by -client-add is
// printed to stdout exactly once — there is no way to recover it later.
func handleClientCommand(args []string, clientsPath string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}

	switch args[0] {
	case "-client-add":
		if len(args) < 2 || args[1] == "" {
			fmt.Fprintln(os.Stderr, "usage: human-relay -client-add <name>")
			return true, 2
		}
		reg, err := auth.NewRegistry(clientsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load client registry: %v\n", err)
			return true, 1
		}
		token, err := reg.Add(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return true, 1
		}
		fmt.Println(token)
		fmt.Fprintln(os.Stderr, "Store this token now: it is shown once and cannot be recovered.")
		return true, 0

	case "-client-list":
		reg, err := auth.NewRegistry(clientsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load client registry: %v\n", err)
			return true, 1
		}
		for _, c := range reg.List() {
			lastSeen := "never"
			if c.LastSeenAt != nil {
				lastSeen = c.LastSeenAt.Format(time.RFC3339)
			}
			status := "active"
			if c.Revoked() {
				status = "revoked"
			}
			fmt.Printf("%s\t%s\t%s\t%s\n", c.Name, c.CreatedAt.Format(time.RFC3339), lastSeen, status)
		}
		return true, 0

	case "-client-revoke":
		if len(args) < 2 || args[1] == "" {
			fmt.Fprintln(os.Stderr, "usage: human-relay -client-revoke <name>")
			return true, 2
		}
		reg, err := auth.NewRegistry(clientsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load client registry: %v\n", err)
			return true, 1
		}
		if err := reg.Revoke(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return true, 1
		}
		return true, 0
	}

	return false, 0
}

// approverDigestFromEnv reads the approver credential from MHR_APPROVER_TOKEN or
// MHR_APPROVER_TOKEN_SHA256 (not both) and returns its SHA-256. It exits on a
// misconfiguration rather than start with approval left open by mistake.
func approverDigestFromEnv() ([sha256.Size]byte, bool) {
	token := os.Getenv("MHR_APPROVER_TOKEN")
	digestHex := os.Getenv("MHR_APPROVER_TOKEN_SHA256")
	switch {
	case token != "" && digestHex != "":
		log.Fatal("set MHR_APPROVER_TOKEN or MHR_APPROVER_TOKEN_SHA256, not both")
	case token != "":
		return sha256.Sum256([]byte(token)), true
	case digestHex != "":
		d, err := auth.ParseTokenDigest(strings.TrimSpace(digestHex))
		if err != nil {
			log.Fatalf("MHR_APPROVER_TOKEN_SHA256: %v", err)
		}
		return d, true
	}
	return [sha256.Size]byte{}, false
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
