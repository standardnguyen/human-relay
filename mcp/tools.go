package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"git.ekaterina.net/administrator/human-relay/audit"
	"git.ekaterina.net/administrator/human-relay/containers"
	"git.ekaterina.net/administrator/human-relay/store"
)

var ToolDefinitions = []Tool{
	{
		Name:        "request_command",
		Description: "Submit a command for human approval. Returns a request ID that can be used to poll for the result.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"command": {
					Type:        "string",
					Description: "The command/binary to execute",
				},
				"args": {
					Type:        "array",
					Description: "Arguments to pass to the command",
					Items:       &Items{Type: "string"},
				},
				"reason": {
					Type:        "string",
					Description: "Why this command needs to be run (shown to the human reviewer)",
				},
				"working_dir": {
					Type:        "string",
					Description: "Working directory for command execution",
				},
				"shell": {
					Type:        "boolean",
					Description: "If true, run via sh -c (allows pipes/redirects but less secure). Default false.",
				},
				"timeout": {
					Type:        "integer",
					Description: "Command timeout in seconds (default: server default, max: server max)",
				},
			},
			Required: []string{"command", "reason"},
		},
	},
	{
		Name:        "get_result",
		Description: "Get the result of a previously submitted command request. Supports blocking poll — if timeout is set, the server will hold the connection until the request is decided or the timeout expires.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"request_id": {
					Type:        "string",
					Description: "The request ID returned by request_command",
				},
				"timeout": {
					Type:        "integer",
					Description: "How long to wait (in seconds) for a decision before returning. Default: 0 (return immediately).",
				},
			},
			Required: []string{"request_id"},
		},
	},
	{
		Name:        "list_requests",
		Description: "List command requests, optionally filtered by status.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"status": {
					Type:        "string",
					Description: "Filter by status",
					Enum:        []string{"pending", "approved", "denied", "running", "complete", "timeout", "error"},
				},
			},
		},
	},
	{
		Name:        "register_container",
		Description: "Register or update a container in the relay's container registry. Instant — no human approval needed.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"ctid": {
					Type:        "integer",
					Description: "Container ID (e.g. 133)",
				},
				"ip": {
					Type:        "string",
					Description: "Container IP address (e.g. 192.168.10.90)",
				},
				"hostname": {
					Type:        "string",
					Description: "Container hostname (e.g. archivebox)",
				},
				"has_relay_ssh": {
					Type:        "boolean",
					Description: "Whether the relay has direct SSH access to this container (default: false)",
					Default:     false,
				},
				"ssh_user": {
					Type:        "string",
					Description: "SSH username for this container (default: root)",
					Default:     "root",
				},
			},
			Required: []string{"ctid", "ip", "hostname"},
		},
	},
	{
		Name:        "list_containers",
		Description: "List all containers in the relay's container registry. Instant — no human approval needed.",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]Property{},
		},
	},
	{
		Name:        "exec_container",
		Description: "Execute a command inside a container. Looks up the container in the registry and routes via direct SSH (if relay has access) or via pct exec on the Proxmox host. Requires human approval.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"ctid": {
					Type:        "integer",
					Description: "Container ID to execute in (e.g. 133)",
				},
				"command": {
					Type:        "string",
					Description: "The command to execute inside the container",
				},
				"args": {
					Type:        "array",
					Description: "Arguments to pass to the command",
					Items:       &Items{Type: "string"},
				},
				"reason": {
					Type:        "string",
					Description: "Why you need to run this command — shown to the human reviewer",
				},
				"shell": {
					Type:        "boolean",
					Description: "If true, wrap command in sh -c on the remote side (default: false)",
				},
				"timeout": {
					Type:        "integer",
					Description: "Command timeout in seconds (default: server default, max: server max)",
				},
			},
			Required: []string{"ctid", "command", "reason"},
		},
	},
	{
		Name:        "write_file",
		Description: "Write a file to a host or container. Content is sent as base64, decoded by the relay, and piped via stdin to avoid shell escaping issues. Requires human approval.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"path": {
					Type:        "string",
					Description: "Absolute path on the target (e.g. /opt/grafana/dashboards/backup-status.json)",
				},
				"content_base64": {
					Type:        "string",
					Description: "File content, base64-encoded",
				},
				"host": {
					Type:        "string",
					Description: "Target IP (default: Proxmox host). Ignored if ctid is set",
				},
				"ctid": {
					Type:        "integer",
					Description: "If set, write to this container (looks up registry for routing)",
				},
				"mode": {
					Type:        "string",
					Description: "File permissions, e.g. \"0755\" (default: \"0644\")",
				},
				"reason": {
					Type:        "string",
					Description: "Why this file needs to be written (shown to the human reviewer)",
				},
				"timeout": {
					Type:        "integer",
					Description: "Command timeout in seconds (default: server default, max: server max)",
				},
			},
			Required: []string{"path", "content_base64", "reason"},
		},
	},
	{
		Name:        "install_relay_ssh",
		Description: "Install the relay's own SSH public key on a container so the relay can SSH directly to it. Always routes through pct exec on the Proxmox host (since the target doesn't have relay SSH yet). After success, automatically updates the container registry with has_relay_ssh=true. Requires human approval.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"ctid": {
					Type:        "integer",
					Description: "Target container ID (e.g. 125)",
				},
				"reason": {
					Type:        "string",
					Description: "Why you need to install the relay's SSH key (shown to the human reviewer)",
				},
				"timeout": {
					Type:        "integer",
					Description: "Command timeout in seconds (default: server default, max: server max)",
				},
			},
			Required: []string{"ctid", "reason"},
		},
	},
	{
		Name:        "install_ssh_key",
		Description: "Install an arbitrary SSH public key on a container. Routes via direct SSH if the relay has access, otherwise via pct exec through the Proxmox host. WARNING: This tool can grant SSH access to any key — use with caution. Requires human approval.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"ctid": {
					Type:        "integer",
					Description: "Target container ID (e.g. 125)",
				},
				"public_key": {
					Type:        "string",
					Description: "The SSH public key to install (e.g. 'ssh-ed25519 AAAA... user@host')",
				},
				"reason": {
					Type:        "string",
					Description: "Why you need to install this SSH key (shown to the human reviewer)",
				},
				"timeout": {
					Type:        "integer",
					Description: "Command timeout in seconds (default: server default, max: server max)",
				},
			},
			Required: []string{"ctid", "public_key", "reason"},
		},
	},
}

type ToolHandler struct {
	store          *store.Store
	containers     *containers.Store
	hostIP         string
	audit          *audit.Logger
	sshConfigFile  string
	relayPubkeyFile string
}

func NewToolHandler(s *store.Store, cs *containers.Store, hostIP string, al *audit.Logger) *ToolHandler {
	return &ToolHandler{store: s, containers: cs, hostIP: hostIP, audit: al}
}

// SetSSHConfig sets a custom SSH config file path. When set, all internally-
// constructed SSH commands will include -F <path>.
func (h *ToolHandler) SetSSHConfig(path string) {
	h.sshConfigFile = path
}

// SetRelayPubkeyFile sets the path to the relay's own SSH public key file.
// Used by install_relay_ssh to read the key to install on target containers.
func (h *ToolHandler) SetRelayPubkeyFile(path string) {
	h.relayPubkeyFile = path
}

// sshPrefix returns the args to prepend before SSH arguments (e.g. ["-F", "/path/to/config"]).
func (h *ToolHandler) sshPrefix() []string {
	if h.sshConfigFile != "" {
		return []string{"-F", h.sshConfigFile}
	}
	return nil
}

func (h *ToolHandler) Handle(name string, args map[string]interface{}) *CallToolResult {
	switch name {
	case "request_command":
		return h.requestCommand(args)
	case "get_result":
		return h.getResult(args)
	case "list_requests":
		return h.listRequests(args)
	case "register_container":
		return h.registerContainer(args)
	case "list_containers":
		return h.listContainers(args)
	case "exec_container":
		return h.execContainer(args)
	case "write_file":
		return h.writeFile(args)
	case "install_relay_ssh":
		return h.installRelaySSH(args)
	case "install_ssh_key":
		return h.installSSHKey(args)
	default:
		return errorResult(fmt.Sprintf("unknown tool: %s", name))
	}
}

func (h *ToolHandler) requestCommand(args map[string]interface{}) *CallToolResult {
	command, _ := args["command"].(string)
	reason, _ := args["reason"].(string)
	if command == "" || reason == "" {
		return errorResult("command and reason are required")
	}

	var cmdArgs []string
	if rawArgs, ok := args["args"].([]interface{}); ok {
		for _, a := range rawArgs {
			if s, ok := a.(string); ok {
				cmdArgs = append(cmdArgs, s)
			}
		}
	}

	workingDir, _ := args["working_dir"].(string)
	shell, _ := args["shell"].(bool)

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	r := h.store.Add(command, cmdArgs, reason, workingDir, shell, timeout)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":        "request_command",
		"command":     command,
		"args":        cmdArgs,
		"reason":      reason,
		"working_dir": workingDir,
		"shell":       shell,
		"timeout":     timeout,
	})

	return textResult(fmt.Sprintf(`{"request_id": "%s", "status": "pending"}`, r.ID))
}

func (h *ToolHandler) getResult(args map[string]interface{}) *CallToolResult {
	requestID, _ := args["request_id"].(string)
	if requestID == "" {
		return errorResult("request_id is required")
	}

	timeoutSec := 0
	if t, ok := args["timeout"].(float64); ok {
		timeoutSec = int(t)
	}

	if timeoutSec > 0 {
		deadline := time.After(time.Duration(timeoutSec) * time.Second)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			r := h.store.Get(requestID)
			if r == nil {
				return errorResult("request not found")
			}
			if r.Status != store.StatusPending {
				return requestResult(r)
			}
			select {
			case <-deadline:
				return requestResult(r)
			case <-ticker.C:
				continue
			}
		}
	}

	r := h.store.Get(requestID)
	if r == nil {
		return errorResult("request not found")
	}
	return requestResult(r)
}

func (h *ToolHandler) listRequests(args map[string]interface{}) *CallToolResult {
	var filter store.Status
	if s, ok := args["status"].(string); ok {
		filter = store.Status(s)
	}

	requests := h.store.List(filter)
	data, _ := json.Marshal(requests)
	return textResult(string(data))
}

func requestResult(r *store.Request) *CallToolResult {
	data, _ := json.Marshal(r)
	return textResult(string(data))
}

func textResult(text string) *CallToolResult {
	return &CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: text}},
	}
}

func errorResult(msg string) *CallToolResult {
	return &CallToolResult{
		Content: []ContentBlock{{Type: "text", Text: msg}},
		IsError: true,
	}
}

func (h *ToolHandler) registerContainer(args map[string]interface{}) *CallToolResult {
	ctid := intArg(args, "ctid")
	if ctid == 0 {
		return errorResult("ctid is required and must be > 0")
	}

	ip, _ := args["ip"].(string)
	if ip == "" {
		return errorResult("ip is required")
	}

	hostname, _ := args["hostname"].(string)
	if hostname == "" {
		return errorResult("hostname is required")
	}

	hasRelaySSH, _ := args["has_relay_ssh"].(bool)
	sshUser, _ := args["ssh_user"].(string)

	c, err := h.containers.Register(ctid, ip, hostname, hasRelaySSH, sshUser)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to register container: %v", err))
	}

	data, _ := json.Marshal(c)
	return textResult(string(data))
}

func (h *ToolHandler) listContainers(args map[string]interface{}) *CallToolResult {
	list, err := h.containers.List()
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list containers: %v", err))
	}
	if list == nil {
		list = []*containers.Container{}
	}
	data, _ := json.Marshal(list)
	return textResult(string(data))
}

func (h *ToolHandler) execContainer(args map[string]interface{}) *CallToolResult {
	ctid := intArg(args, "ctid")
	if ctid == 0 {
		return errorResult("ctid is required and must be > 0")
	}

	command, _ := args["command"].(string)
	if command == "" {
		return errorResult("command is required")
	}

	reason, _ := args["reason"].(string)
	if reason == "" {
		return errorResult("reason is required")
	}

	shell, _ := args["shell"].(bool)

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	var cmdArgs []string
	if rawArgs, ok := args["args"].([]interface{}); ok {
		for _, a := range rawArgs {
			if s, ok := a.(string); ok {
				cmdArgs = append(cmdArgs, s)
			}
		}
	}

	// Look up container
	c, err := h.containers.Get(ctid)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to look up container: %v", err))
	}
	if c == nil {
		return errorResult(fmt.Sprintf("container %d not found in registry. Use register_container first.", ctid))
	}

	// Determine SSH user for direct container access
	sshUser := "root"
	if c.SSHUser != "" {
		sshUser = c.SSHUser
	}

	// Build the SSH command
	sshArgs := append([]string{}, h.sshPrefix()...)
	if c.HasRelaySSH {
		// Direct SSH to container — SSH passes remote args to the remote
		// login shell, so we don't need sh -c for shell mode.
		sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", sshUser, c.IP), "--")
		if shell {
			full := command
			for _, a := range cmdArgs {
				full += " " + a
			}
			sshArgs = append(sshArgs, full)
		} else {
			sshArgs = append(sshArgs, command)
			sshArgs = append(sshArgs, cmdArgs...)
		}
	} else {
		// Fallback: SSH to host, then pct exec. Shell-quote the command
		// so the host's shell passes it intact to pct exec → container.
		sshArgs = append(sshArgs, fmt.Sprintf("root@%s", h.hostIP), "pct", "exec", fmt.Sprintf("%d", ctid), "--")
		if shell {
			full := command
			for _, a := range cmdArgs {
				full += " " + a
			}
			sshArgs = append(sshArgs, "sh", "-c", shellQuote(full))
		} else {
			sshArgs = append(sshArgs, command)
			sshArgs = append(sshArgs, cmdArgs...)
		}
	}

	// Prefix reason with container context
	prefixedReason := fmt.Sprintf("[CTID %d %s] %s", c.CTID, c.Hostname, reason)

	// Create a regular approval request through the store
	r := h.store.Add("ssh", sshArgs, prefixedReason, "", false, timeout)

	route := "direct_ssh"
	if !c.HasRelaySSH {
		route = "pct_exec"
	}

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":     "exec_container",
		"ctid":     ctid,
		"hostname": c.Hostname,
		"command":  command,
		"args":     cmdArgs,
		"reason":   reason,
		"route":    route,
		"shell":    shell,
		"timeout":  timeout,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
		"container":  fmt.Sprintf("CTID %d (%s)", c.CTID, c.Hostname),
		"route":      route,
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}

// intArg extracts an integer from args, handling both float64 (JSON default) and direct int.
func intArg(args map[string]interface{}, key string) int {
	if f, ok := args[key].(float64); ok {
		return int(f)
	}
	if i, ok := args[key].(int); ok {
		return i
	}
	return 0
}

var validPathRe = regexp.MustCompile(`^/[a-zA-Z0-9._/\-]+$`)
var validModeRe = regexp.MustCompile(`^0[0-7]{3}$`)

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func (h *ToolHandler) writeFile(args map[string]interface{}) *CallToolResult {
	path, _ := args["path"].(string)
	contentB64, _ := args["content_base64"].(string)
	reason, _ := args["reason"].(string)

	if path == "" || contentB64 == "" || reason == "" {
		return errorResult("path, content_base64, and reason are required")
	}

	if !validPathRe.MatchString(path) {
		return errorResult("path must be absolute with only alphanumeric, dot, dash, underscore, and slash characters")
	}

	// Decode base64 (try standard, then raw/no-padding)
	contentB64 = strings.Join(strings.Fields(contentB64), "")
	content, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		content, err = base64.RawStdEncoding.DecodeString(contentB64)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid base64: %v", err))
		}
	}

	mode := "0644"
	if m, ok := args["mode"].(string); ok && m != "" {
		mode = m
	}
	if !validModeRe.MatchString(mode) {
		return errorResult("mode must be an octal permission string like 0644 or 0755")
	}

	ctid := intArg(args, "ctid")
	host, _ := args["host"].(string)
	if host == "" {
		host = h.hostIP
	}

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	pfx := h.sshPrefix()
	var sshArgs []string
	var target string
	var route string

	if ctid > 0 {
		c, err := h.containers.Get(ctid)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to look up container: %v", err))
		}
		if c == nil {
			return errorResult(fmt.Sprintf("container %d not found in registry. Use register_container first.", ctid))
		}
		target = fmt.Sprintf("CTID %d (%s)", c.CTID, c.Hostname)

		if c.HasRelaySSH {
			route = "direct_ssh"
			wfUser := "root"
			if c.SSHUser != "" {
				wfUser = c.SSHUser
			}
			shellCmd := fmt.Sprintf("cat > %s && chmod %s %s", shellQuote(path), mode, shellQuote(path))
			sshArgs = append(pfx, fmt.Sprintf("%s@%s", wfUser, c.IP), "--", shellCmd)
		} else {
			route = "pct_push"
			tmpFile := fmt.Sprintf("/tmp/mhr-%d", time.Now().UnixNano())
			shellCmd := fmt.Sprintf("cat > %s && pct push %d %s %s && pct exec %d -- chmod %s %s && rm %s",
				tmpFile, c.CTID, tmpFile, shellQuote(path), c.CTID, mode, shellQuote(path), tmpFile)
			sshArgs = append(pfx, fmt.Sprintf("root@%s", h.hostIP), "--", "sh", "-c", shellCmd)
		}
	} else {
		target = host
		route = "direct_ssh"
		shellCmd := fmt.Sprintf("cat > %s && chmod %s %s", shellQuote(path), mode, shellQuote(path))
		sshArgs = append(pfx, fmt.Sprintf("root@%s", host), "--", shellCmd)
	}

	// Build content preview for the human reviewer
	preview := string(content)
	if len(preview) > 2048 {
		preview = preview[:2048] + "\n... (truncated)"
	}
	prefixedReason := fmt.Sprintf("[FILE %dB -> %s:%s] %s\n---\n%s", len(content), target, path, reason, preview)

	r := h.store.AddWithStdin("ssh", sshArgs, prefixedReason, "", false, timeout, content)

	displayCmd := fmt.Sprintf("write -> %s:%s  [%dB, mode %s]", target, path, len(content), mode)
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":   "write_file",
		"target": target,
		"path":   path,
		"size":   len(content),
		"mode":   mode,
		"route":  route,
		"reason": reason,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
		"target":     target,
		"path":       path,
		"size":       len(content),
		"route":      route,
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}

// sshKeyLine ensures the key is a single line with a trailing newline,
// suitable for appending to authorized_keys.
func sshKeyLine(key string) string {
	return strings.TrimSpace(key) + "\n"
}

// validSSHKeyRe matches common SSH public key formats.
var validSSHKeyRe = regexp.MustCompile(`^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)) [A-Za-z0-9+/=]+ ?\S*$`)

func (h *ToolHandler) installRelaySSH(args map[string]interface{}) *CallToolResult {
	ctid := intArg(args, "ctid")
	if ctid == 0 {
		return errorResult("ctid is required and must be > 0")
	}
	reason, _ := args["reason"].(string)
	if reason == "" {
		return errorResult("reason is required")
	}

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	// Read the relay's public key
	if h.relayPubkeyFile == "" {
		return errorResult("relay public key file not configured (set MHR_RELAY_PUBKEY_FILE)")
	}
	pubkeyBytes, err := os.ReadFile(h.relayPubkeyFile)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to read relay public key from %s: %v", h.relayPubkeyFile, err))
	}
	pubkey := strings.TrimSpace(string(pubkeyBytes))
	if !validSSHKeyRe.MatchString(pubkey) {
		return errorResult(fmt.Sprintf("relay public key file does not contain a valid SSH public key"))
	}

	// Look up container in registry (or allow unregistered — we'll register after)
	c, err := h.containers.Get(ctid)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to look up container: %v", err))
	}

	// Build the write command. Always route through pct push on the host,
	// since the whole point is the target doesn't have relay SSH yet.
	pfx := h.sshPrefix()
	content := []byte(sshKeyLine(pubkey))
	tmpFile := fmt.Sprintf("/tmp/mhr-key-%d", time.Now().UnixNano())

	// mkdir -p /root/.ssh on the container, then push the key to a temp file, then append, then chmod
	shellCmd := fmt.Sprintf(
		"cat > %s && pct exec %d -- mkdir -p /root/.ssh && pct push %d %s %s --perms 0600 && "+
			"pct exec %d -- sh -c 'cat %s >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && chmod 700 /root/.ssh' && rm %s",
		tmpFile, ctid, ctid, tmpFile, tmpFile,
		ctid, tmpFile, tmpFile,
	)

	sshArgs := append(pfx, fmt.Sprintf("root@%s", h.hostIP), "--", "sh", "-c", shellCmd)

	hostname := fmt.Sprintf("ctid-%d", ctid)
	if c != nil {
		hostname = c.Hostname
	}
	prefixedReason := fmt.Sprintf("[SSH KEY -> CTID %d %s] %s\n---\nInstalling relay's own public key for direct SSH access.\nKey: %s", ctid, hostname, reason, pubkey)

	r := h.store.AddWithStdin("ssh", sshArgs, prefixedReason, "", false, timeout, content)

	displayCmd := fmt.Sprintf("install relay SSH key -> CTID %d (%s)", ctid, hostname)
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":     "install_relay_ssh",
		"ctid":     ctid,
		"hostname": hostname,
		"reason":   reason,
	})

	result := map[string]interface{}{
		"request_id":  r.ID,
		"status":      "pending",
		"container":   fmt.Sprintf("CTID %d (%s)", ctid, hostname),
		"route":       "pct_push",
		"auto_register": true,
		"note":        "After approval and successful execution, call register_container with has_relay_ssh=true (or update existing registration).",
	}

	// If container is already registered, note it; auto-update happens post-execution
	// (we can't update now since the command hasn't run yet)
	if c != nil {
		result["already_registered"] = true
		result["note"] = fmt.Sprintf("Container already registered. After approval, update registration with has_relay_ssh=true.")
	}

	data, _ := json.Marshal(result)
	return textResult(string(data))
}

func (h *ToolHandler) installSSHKey(args map[string]interface{}) *CallToolResult {
	ctid := intArg(args, "ctid")
	if ctid == 0 {
		return errorResult("ctid is required and must be > 0")
	}
	publicKey, _ := args["public_key"].(string)
	if publicKey == "" {
		return errorResult("public_key is required")
	}
	reason, _ := args["reason"].(string)
	if reason == "" {
		return errorResult("reason is required")
	}

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	pubkey := strings.TrimSpace(publicKey)
	if !validSSHKeyRe.MatchString(pubkey) {
		return errorResult("public_key does not look like a valid SSH public key (expected format: ssh-ed25519 AAAA... comment)")
	}

	// Look up container
	c, err := h.containers.Get(ctid)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to look up container: %v", err))
	}
	if c == nil {
		return errorResult(fmt.Sprintf("container %d not found in registry. Use register_container first.", ctid))
	}

	pfx := h.sshPrefix()
	content := []byte(sshKeyLine(pubkey))
	var sshArgs []string
	var route string

	if c.HasRelaySSH {
		// Direct SSH: pipe the key and append to authorized_keys
		route = "direct_ssh"
		keyUser := "root"
		if c.SSHUser != "" {
			keyUser = c.SSHUser
		}
		shellCmd := "mkdir -p /root/.ssh && cat >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && chmod 700 /root/.ssh"
		sshArgs = append(pfx, fmt.Sprintf("%s@%s", keyUser, c.IP), "--", "sh", "-c", shellCmd)
	} else {
		// pct push fallback
		route = "pct_push"
		tmpFile := fmt.Sprintf("/tmp/mhr-key-%d", time.Now().UnixNano())
		shellCmd := fmt.Sprintf(
			"cat > %s && pct exec %d -- mkdir -p /root/.ssh && pct push %d %s %s --perms 0600 && "+
				"pct exec %d -- sh -c 'cat %s >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && chmod 700 /root/.ssh' && rm %s",
			tmpFile, ctid, ctid, tmpFile, tmpFile,
			ctid, tmpFile, tmpFile,
		)
		sshArgs = append(pfx, fmt.Sprintf("root@%s", h.hostIP), "--", "sh", "-c", shellCmd)
	}

	prefixedReason := fmt.Sprintf("[SSH KEY -> CTID %d %s] %s\n---\nKey: %s", ctid, c.Hostname, reason, pubkey)

	r := h.store.AddWithStdin("ssh", sshArgs, prefixedReason, "", false, timeout, content)

	displayCmd := fmt.Sprintf("install SSH key -> CTID %d (%s)", ctid, c.Hostname)
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":     "install_ssh_key",
		"ctid":     ctid,
		"hostname": c.Hostname,
		"key":      pubkey,
		"reason":   reason,
		"route":    route,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
		"container":  fmt.Sprintf("CTID %d (%s)", ctid, c.Hostname),
		"route":      route,
		"warning":    "This installs an arbitrary SSH key. Verify the key belongs to a trusted party before approving.",
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}
