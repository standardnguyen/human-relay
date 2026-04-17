package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/standardnguyen/human-relay/audit"
	"github.com/standardnguyen/human-relay/containers"
	"github.com/standardnguyen/human-relay/store"
)

func boolPtr(b bool) *bool { return &b }

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
		Annotations: &ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(true)},
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
		Annotations: &ToolAnnotations{ReadOnlyHint: boolPtr(true)},
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
		Annotations: &ToolAnnotations{ReadOnlyHint: boolPtr(true)},
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
		Annotations: &ToolAnnotations{IdempotentHint: boolPtr(true)},
	},
	{
		Name:        "list_containers",
		Description: "List all containers in the relay's container registry. Instant — no human approval needed.",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]Property{},
		},
		Annotations: &ToolAnnotations{ReadOnlyHint: boolPtr(true)},
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
		Annotations: &ToolAnnotations{OpenWorldHint: boolPtr(true)},
	},
	{
		Name:        "write_file",
		Description: "Write a file to a host or container. Content is piped via stdin (never on the shell command line), so quotes, backticks, $, and newlines pass through unharmed. Pass plain text as `content`, or raw binary as `content_base64`. Exactly one is required. Requires human approval.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"path": {
					Type:        "string",
					Description: "Absolute path on the target (e.g. /opt/grafana/dashboards/backup-status.json)",
				},
				"content": {
					Type:        "string",
					Description: "File content as plain text (UTF-8). Preferred for config files, scripts, and other text. Use content_base64 for raw binary.",
				},
				"content_base64": {
					Type:        "string",
					Description: "File content, base64-encoded. Use for raw binary files or content with non-UTF-8 bytes. For text, use `content` instead.",
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
			Required: []string{"path", "reason"},
		},
		Annotations: &ToolAnnotations{OpenWorldHint: boolPtr(true)},
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
		Annotations: &ToolAnnotations{OpenWorldHint: boolPtr(true)},
	},
	{
		Name:        "http_request",
		Description: "Make an HTTP request (GET, POST, PUT, PATCH, DELETE) through the human approval flow. The human reviewer sees the full request details before approving. Useful for API calls (Trello, Slack, etc.) where you want human-in-the-loop control.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"method": {
					Type:        "string",
					Description: "HTTP method",
					Enum:        []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"},
				},
				"url": {
					Type:        "string",
					Description: "The full URL to request (e.g. https://api.trello.com/1/cards)",
				},
				"headers": {
					Type:        "object",
					Description: "HTTP headers as key-value pairs (e.g. {\"Authorization\": \"Bearer token\", \"Content-Type\": \"application/json\"})",
				},
				"body": {
					Type:        "string",
					Description: "Request body (typically JSON). Omit for GET/DELETE/HEAD requests.",
				},
				"reason": {
					Type:        "string",
					Description: "Why this request needs to be made (shown to the human reviewer)",
				},
				"timeout": {
					Type:        "integer",
					Description: "Request timeout in seconds (default: server default, max: server max)",
				},
			},
			Required: []string{"method", "url", "reason"},
		},
		Annotations: &ToolAnnotations{OpenWorldHint: boolPtr(true)},
	},
	{
		Name:        "run_script",
		Description: "Run a named script on the relay. Supports shell scripts (.sh), Python scripts (.py), and JSON pipeline definitions (.json). Lookup order: .sh, .py, .json. Environment variables are available in all script types. Requires human approval.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"name": {
					Type:        "string",
					Description: "Script name (e.g. 'queue-to-doing'). Must match a file at /scripts/{name}.sh on the relay.",
				},
				"args": {
					Type:        "array",
					Description: "Arguments to pass to the script. For shell/Python: positional args ($1, $2 / sys.argv). For JSON pipelines: available as ${1}, ${2}, etc.",
					Items:       &Items{Type: "string"},
				},
				"reason": {
					Type:        "string",
					Description: "Why this script needs to run (shown to the human reviewer)",
				},
				"timeout": {
					Type:        "integer",
					Description: "Script timeout in seconds (default: server default, max: server max)",
				},
			},
			Required: []string{"name", "reason"},
		},
		Annotations: &ToolAnnotations{OpenWorldHint: boolPtr(true)},
	},
	{
		Name:        "create_script",
		Description: "Create or update a script on the relay. The human reviewer sees the full content before approving. Supports JSON pipelines, Python scripts, and shell scripts. Type is auto-detected: JSON objects become .json, shebang lines (#!/bin/bash, #!/bin/sh) become .sh, everything else becomes .py. Use run_script to execute them.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"name": {
					Type:        "string",
					Description: "Script name (e.g. 'queue-to-doing'). Alphanumeric, hyphens, and underscores only.",
				},
				"content": {
					Type:        "string",
					Description: "The full script content. JSON objects are saved as .json pipelines, scripts starting with #!/bin/bash or #!/bin/sh as .sh, everything else as .py.",
				},
				"reason": {
					Type:        "string",
					Description: "Why this script is being created/updated (shown to the human reviewer)",
				},
			},
			Required: []string{"name", "content", "reason"},
		},
		Annotations: &ToolAnnotations{OpenWorldHint: boolPtr(true)},
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
		Annotations: &ToolAnnotations{DestructiveHint: boolPtr(false), OpenWorldHint: boolPtr(true)},
	},
	{
		Name:        "withdraw_request",
		Description: "Retract a pending request before a human has decided on it. Use this when the agent realizes a submitted request was wrong (typo, stale plan, wrong target) and wants to prevent execution without waiting for the human to deny it. The request stays visible in the dashboard as WITHDRAWN with the supplied reason, but approve/deny/whitelist buttons are replaced with Mark Read. Only pending requests can be withdrawn; approved/running/complete/denied requests return an error.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"request_id": {
					Type:        "string",
					Description: "The request ID returned by a previous tool call (request_command, write_file, http_request, run_script, etc.)",
				},
				"reason": {
					Type:        "string",
					Description: "Why the request is being withdrawn. Shown to the human reviewer alongside the WITHDRAWN marker.",
				},
			},
			Required: []string{"request_id", "reason"},
		},
		Annotations: &ToolAnnotations{OpenWorldHint: boolPtr(false)},
	},
}

type ToolHandler struct {
	store          *store.Store
	containers     *containers.Store
	hostIP         string
	audit          *audit.Logger
	sshConfigFile  string
	relayPubkeyFile string
	scriptsDir     string
}

func NewToolHandler(s *store.Store, cs *containers.Store, hostIP string, al *audit.Logger) *ToolHandler {
	return &ToolHandler{store: s, containers: cs, hostIP: hostIP, audit: al, scriptsDir: "/scripts"}
}

// SetScriptsDir overrides the directory where run_script looks for scripts.
func (h *ToolHandler) SetScriptsDir(dir string) {
	h.scriptsDir = dir
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
	case "http_request":
		return h.httpRequest(args)
	case "run_script":
		return h.runScript(args)
	case "create_script":
		return h.createScript(args)
	case "install_relay_ssh":
		return h.installRelaySSH(args)
	case "install_ssh_key":
		return h.installSSHKey(args)
	case "withdraw_request":
		return h.withdrawRequest(args)
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

	// Hard rejection for bash/sh -c arg-splitting (non-shell mode only;
	// shell mode gets an advisory warning in detectWarnings).
	if !shell {
		if errMsg := checkBashCArgSplitting(command, cmdArgs); errMsg != "" {
			return errorResult(errMsg)
		}
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

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
	}
	if warnings := detectWarnings(command, cmdArgs, shell); len(warnings) > 0 {
		result["warnings"] = warnings
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
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

	// Hard rejection for bash/sh -c arg-splitting (non-shell mode only;
	// shell mode gets an advisory warning in detectWarnings).
	if !shell {
		if errMsg := checkBashCArgSplitting(command, cmdArgs); errMsg != "" {
			return errorResult(errMsg)
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

func (h *ToolHandler) httpRequest(args map[string]interface{}) *CallToolResult {
	method, _ := args["method"].(string)
	url, _ := args["url"].(string)
	reason, _ := args["reason"].(string)

	if method == "" || url == "" || reason == "" {
		return errorResult("method, url, and reason are required")
	}

	// Validate method
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
	default:
		return errorResult(fmt.Sprintf("unsupported HTTP method: %s (use GET, POST, PUT, PATCH, DELETE, or HEAD)", method))
	}

	// Basic URL validation
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return errorResult("url must start with http:// or https://")
	}

	// Parse headers
	var headers map[string]string
	if rawHeaders, ok := args["headers"].(map[string]interface{}); ok {
		headers = make(map[string]string)
		for k, v := range rawHeaders {
			if s, ok := v.(string); ok {
				headers[k] = s
			}
		}
	}

	body, _ := args["body"].(string)

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	r := h.store.AddHTTP(method, url, headers, body, reason, timeout)

	// Build a friendly display command
	displayCmd := fmt.Sprintf("%s %s", method, url)
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":    "http_request",
		"method":  method,
		"url":     url,
		"headers": headerKeys(headers),
		"has_body": body != "",
		"reason":  reason,
		"timeout": timeout,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}

// validScriptNameRe matches safe script names: alphanumeric, hyphens, underscores.
var validScriptNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func (h *ToolHandler) runScript(args map[string]interface{}) *CallToolResult {
	name, _ := args["name"].(string)
	reason, _ := args["reason"].(string)

	if name == "" || reason == "" {
		return errorResult("name and reason are required")
	}

	if !validScriptNameRe.MatchString(name) {
		return errorResult("script name must be alphanumeric with hyphens/underscores only (no paths, no extensions)")
	}

	// Check that the script file exists (.sh, .py, or .json)
	shPath := fmt.Sprintf("%s/%s.sh", h.scriptsDir, name)
	pyPath := fmt.Sprintf("%s/%s.py", h.scriptsDir, name)
	jsonPath := fmt.Sprintf("%s/%s.json", h.scriptsDir, name)
	_, shErr := os.Stat(shPath)
	_, pyErr := os.Stat(pyPath)
	_, jsonErr := os.Stat(jsonPath)
	if os.IsNotExist(shErr) && os.IsNotExist(pyErr) && os.IsNotExist(jsonErr) {
		return errorResult(fmt.Sprintf("script not found: tried %s, %s, and %s", shPath, pyPath, jsonPath))
	}

	var scriptArgs []string
	if rawArgs, ok := args["args"].([]interface{}); ok {
		for _, a := range rawArgs {
			if s, ok := a.(string); ok {
				scriptArgs = append(scriptArgs, s)
			}
		}
	}

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	r := h.store.AddScript(name, scriptArgs, reason, timeout)

	displayCmd := fmt.Sprintf("run_script %s", name)
	if len(scriptArgs) > 0 {
		displayCmd += " " + strings.Join(scriptArgs, " ")
	}
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":    "run_script",
		"script":  name,
		"args":    scriptArgs,
		"reason":  reason,
		"timeout": timeout,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}

func (h *ToolHandler) createScript(args map[string]interface{}) *CallToolResult {
	name, _ := args["name"].(string)
	content, _ := args["content"].(string)
	reason, _ := args["reason"].(string)

	if name == "" || content == "" || reason == "" {
		return errorResult("name, content, and reason are required")
	}

	if !validScriptNameRe.MatchString(name) {
		return errorResult("script name must be alphanumeric with hyphens/underscores only (no paths, no extensions)")
	}

	// Build a reason that shows the full script content for human review
	preview := content
	if len(preview) > 4096 {
		preview = preview[:4096] + "\n... (truncated)"
	}
	ext := ".py"
	var obj map[string]interface{}
	if json.Unmarshal([]byte(content), &obj) == nil {
		ext = ".json"
	} else if strings.HasPrefix(content, "#!/bin/bash") ||
		strings.HasPrefix(content, "#!/bin/sh") ||
		strings.HasPrefix(content, "#!/usr/bin/env bash") ||
		strings.HasPrefix(content, "#!/usr/bin/env sh") {
		ext = ".sh"
	}
	prefixedReason := fmt.Sprintf("[SCRIPT %s%s %dB] %s\n---\n%s", name, ext, len(content), reason, preview)

	r := h.store.AddScript(name, nil, prefixedReason, 0)
	r.Type = "script_create"
	// Store script content for execution (writing to disk)
	h.store.SetStdin(r.ID, []byte(content))

	displayCmd := fmt.Sprintf("create_script %s (%dB)", name, len(content))
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":   "create_script",
		"script": name,
		"size":   len(content),
		"reason": reason,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}

// headerKeys returns just the header names (not values) for audit logging,
// avoiding leaking auth tokens into the audit log.
func headerKeys(headers map[string]string) []string {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	return keys
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

// bashCShellRe matches "bash -c word word" or "sh -c word word" patterns in a
// concatenated shell command string. The first word after -c is an unquoted
// single-word command, followed by another word — indicating arg-splitting.
// Does NOT match when the command after -c starts with a quote (properly quoted).
var bashCShellRe = regexp.MustCompile(`\b(?:bash|sh)\s+-c\s+([^\s'"` + "`" + `]+)\s+\S+`)

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// shellMetachars are characters/sequences that only work when interpreted by a shell.
var shellMetachars = []string{">>", "||", "&&", "|", ";", ">", "<"}

// checkBashCArgSplitting detects the bash/sh -c arg-splitting anti-pattern where
// `bash -c word extra_args...` only executes `word` — the extra args silently
// become positional parameters ($0, $1, ...) instead of part of the command.
// Returns an error string if detected, empty string if safe.
//
// This has caused production incidents: `bash -c crontab -l` runs bare `crontab`
// (which reads empty stdin and wipes the crontab) instead of `crontab -l`.
func checkBashCArgSplitting(command string, args []string) string {
	// Case 1: command itself is bash/sh with -c
	if (command == "bash" || command == "sh") && len(args) >= 2 && args[0] == "-c" {
		cmdStr := args[1]
		if !strings.Contains(cmdStr, " ") && len(args) >= 3 {
			return fmt.Sprintf(
				"BLOCKED: %s -c arg-splitting — '%s -c %s %s' only executes '%s', "+
					"the remaining args become positional parameters ($0, $1, ...) and are NOT "+
					"part of the command. This can cause destructive behavior (e.g., bare 'crontab' "+
					"wipes the crontab). Fix: quote the full command as a single arg: "+
					"%s -c '%s'",
				command, command, cmdStr, strings.Join(args[2:], " "), cmdStr,
				command, strings.Join(args[1:], " "))
		}
	}

	// Case 2: bash/sh -c appears somewhere in the args (e.g., SSH remote command)
	for i := 0; i < len(args)-2; i++ {
		if (args[i] == "bash" || args[i] == "sh") && args[i+1] == "-c" {
			cmdStr := args[i+2]
			if !strings.Contains(cmdStr, " ") && i+3 < len(args) {
				return fmt.Sprintf(
					"BLOCKED: %s -c arg-splitting — '%s -c %s %s' only executes '%s', "+
						"the remaining args become positional parameters ($0, $1, ...) and are NOT "+
						"part of the command. This can cause destructive behavior (e.g., bare 'crontab' "+
						"wipes the crontab). Fix: quote the full command as a single arg: "+
						"%s -c '%s'",
					args[i], args[i], cmdStr, args[i+3], cmdStr,
					args[i], strings.Join(args[i+2:], " "))
			}
			break
		}
	}

	return ""
}

// detectWarnings returns advisory warnings for command patterns that are likely
// to produce unexpected results. The command still proceeds — these are hints
// to help agents correct their approach.
func detectWarnings(command string, args []string, shell bool) []string {
	var warnings []string

	// 1. Shell metacharacters in non-shell mode args
	if !shell {
		for _, arg := range args {
			for _, mc := range shellMetachars {
				if strings.Contains(arg, mc) {
					warnings = append(warnings, fmt.Sprintf(
						"args contain shell metacharacter %q but shell=false -- "+
							"it will be passed as a literal string, not interpreted by a shell. "+
							"Use write_file for file operations, or set shell=true if you need shell features.",
						mc))
					goto doneMetachar // one warning is enough
				}
			}
		}
	}
doneMetachar:

	// 2. bash -c / sh -c arg-splitting warning (shell mode — string-based detection).
	// Non-shell mode is caught by checkBashCArgSplitting() as a hard error.
	// For shell mode, the args are concatenated into a single string, so we
	// do a best-effort regex check for the pattern in the full command.
	if shell && len(args) > 0 {
		full := command + " " + strings.Join(args, " ")
		if bashCShellRe.MatchString(full) {
			warnings = append(warnings,
				"possible bash/sh -c arg-splitting in shell command: when the remote shell "+
					"processes 'bash -c word extra_args', only 'word' is executed — the rest become "+
					"positional parameters. This can be destructive (e.g., bare 'crontab' wipes the crontab). "+
					"Ensure the command after -c is properly quoted as a single string.")
		}
	}

	// 3. shell:true with SSH + redirects (redirect runs on relay, not remote)
	if shell && len(args) > 0 {
		full := command + " " + strings.Join(args, " ")
		if strings.Contains(full, "ssh ") || strings.HasPrefix(command, "ssh") {
			for _, redir := range []string{">>", ">"} {
				if strings.Contains(full, redir) {
					warnings = append(warnings,
						"shell=true with SSH + redirect: the redirect ("+redir+") executes on the relay machine, "+
							"not the remote host. The remote command's output is being redirected locally. "+
							"Use write_file instead, or pass the full command including redirect as a single SSH arg in non-shell mode.")
					break
				}
			}
		}
	}

	return warnings
}

func (h *ToolHandler) writeFile(args map[string]interface{}) *CallToolResult {
	path, _ := args["path"].(string)
	reason, _ := args["reason"].(string)

	if path == "" || reason == "" {
		return errorResult("path and reason are required")
	}

	if !validPathRe.MatchString(path) {
		return errorResult("path must be absolute with only alphanumeric, dot, dash, underscore, and slash characters")
	}

	// Content can arrive as plain text (`content`) or base64 (`content_base64`).
	// Exactly one is required: the plaintext path avoids a round-trip for text
	// files while content_base64 remains available for raw binary / non-UTF-8.
	plaintext, hasPlain := args["content"].(string)
	contentB64, hasB64 := args["content_base64"].(string)
	if hasPlain && plaintext != "" && hasB64 && contentB64 != "" {
		return errorResult("provide either content or content_base64, not both")
	}
	var content []byte
	switch {
	case hasPlain && plaintext != "":
		content = []byte(plaintext)
	case hasB64 && contentB64 != "":
		// Decode base64 (try standard, then raw/no-padding)
		contentB64 = strings.Join(strings.Fields(contentB64), "")
		decoded, err := base64.StdEncoding.DecodeString(contentB64)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(contentB64)
			if err != nil {
				return errorResult(fmt.Sprintf("invalid base64: %v", err))
			}
		}
		content = decoded
	default:
		return errorResult("content or content_base64 is required")
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

func (h *ToolHandler) withdrawRequest(args map[string]interface{}) *CallToolResult {
	requestID, _ := args["request_id"].(string)
	reason, _ := args["reason"].(string)

	if requestID == "" || reason == "" {
		return errorResult("request_id and reason are required")
	}

	ok, currentStatus := h.store.Withdraw(requestID, reason)
	if !ok {
		if currentStatus == store.StatusPending {
			return errorResult(fmt.Sprintf("request %s not found", requestID))
		}
		return errorResult(fmt.Sprintf("request %s is %s, only pending requests can be withdrawn", requestID, currentStatus))
	}

	h.audit.Log("request_withdrawn", requestID, map[string]interface{}{
		"tool":            "withdraw_request",
		"withdraw_reason": reason,
	})

	result := map[string]interface{}{
		"request_id": requestID,
		"status":     string(store.StatusWithdrawn),
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}
