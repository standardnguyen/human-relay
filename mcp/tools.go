package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/standardnguyen/human-relay/audit"
	"github.com/standardnguyen/human-relay/containers"
	"github.com/standardnguyen/human-relay/machines"
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
		Name:        "register_machine",
		Description: "Register or update a non-LXC SSH target (Windows workstation, bare-metal host, VM, WSL instance) in the relay's machine registry, keyed by a string name. This is the first-class home for SSH targets that aren't Proxmox containers — use it instead of registering a fake 'pseudo-CTID' in the container registry. Instant — no human approval needed.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"name": {
					Type:        "string",
					Description: "Machine name — the lookup key (e.g. 'corsair-win'). Alphanumeric with hyphens/underscores.",
				},
				"host": {
					Type:        "string",
					Description: "IP or hostname the relay SSHes to (e.g. 100.106.181.59 or corsair.tailnet.ts.net)",
				},
				"ssh_user": {
					Type:        "string",
					Description: "SSH login user on the machine (required — unlike containers there is no root default)",
				},
				"shell": {
					Type:        "string",
					Description: "Remote shell for command construction: 'posix' (default — sh/bash, Linux/macOS/WSL) or 'powershell' (Windows). Drives how exec/write_file/key-install build the remote command.",
					Enum:        []string{"posix", "powershell"},
					Default:     "posix",
				},
				"identity_file": {
					Type:        "string",
					Description: "Optional path to a specific SSH private key on the relay (e.g. /root/.ssh-data/id_rsa). Omit to use the relay's default key.",
				},
			},
			Required: []string{"name", "host", "ssh_user"},
		},
		Annotations: &ToolAnnotations{IdempotentHint: boolPtr(true)},
	},
	{
		Name:        "list_machines",
		Description: "List all machines in the relay's machine registry. Instant — no human approval needed.",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]Property{},
		},
		Annotations: &ToolAnnotations{ReadOnlyHint: boolPtr(true)},
	},
	{
		Name:        "delete_machine",
		Description: "Remove a machine from the relay's machine registry. Instant — no human approval needed.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"name": {
					Type:        "string",
					Description: "Machine name to remove",
				},
			},
			Required: []string{"name"},
		},
		Annotations: &ToolAnnotations{IdempotentHint: boolPtr(true)},
	},
	{
		Name:        "exec_machine",
		Description: "Execute a command on a registered machine (non-LXC SSH target). Looks up the machine in the registry and routes via direct SSH, building the remote command for the machine's shell (posix or powershell). For powershell machines, shell=true runs the command through PowerShell via -EncodedCommand (the dashboard decodes it for the reviewer). Requires human approval.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"machine": {
					Type:        "string",
					Description: "Machine name to execute on (e.g. 'corsair-win')",
				},
				"command": {
					Type:        "string",
					Description: "The command/binary to execute on the machine",
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
					Description: "If true, run the command through the machine's shell (sh -c for posix; PowerShell -EncodedCommand for powershell). Default false (direct binary invocation).",
				},
				"timeout": {
					Type:        "integer",
					Description: "Command timeout in seconds (default: server default, max: server max)",
				},
			},
			Required: []string{"machine", "command", "reason"},
		},
		Annotations: &ToolAnnotations{OpenWorldHint: boolPtr(true)},
	},
	{
		Name:        "write_file",
		Description: "Write a file to a host or container. Content is piped via stdin (never on the shell command line), so quotes, backticks, $, and newlines pass through unharmed. Pass plain text as `content`, raw binary as `content_base64`, or stream from a remote host with `source_path` (+ `source_host`/`source_ctid`) so the bytes never transit the agent context. Exactly one content method is required. Requires human approval.",
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
				"source_path": {
					Type:        "string",
					Description: "Stream the file from this absolute path on source_host/source_ctid (default: Proxmox host) instead of passing content inline. The bytes flow source -> relay -> destination over SSH and never transit the agent context. Mutually exclusive with content/content_base64.",
				},
				"source_host": {
					Type:        "string",
					Description: "Source IP to stream from (requires source_path). Mutually exclusive with source_ctid.",
				},
				"source_ctid": {
					Type:        "integer",
					Description: "Source container to stream from (requires source_path; registry routing: direct SSH, or pct exec via the Proxmox host). Mutually exclusive with source_host.",
				},
				"host": {
					Type:        "string",
					Description: "Target IP (default: Proxmox host). Ignored if ctid is set",
				},
				"ctid": {
					Type:        "integer",
					Description: "If set, write to this container (looks up registry for routing)",
				},
				"machine": {
					Type:        "string",
					Description: "If set, write to this registered machine (non-LXC SSH target; takes precedence over host). For powershell machines the content is base64-streamed over stdin and decoded on the far side (binary-safe); mode is ignored (Windows uses ACLs).",
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
					Description: "Request body (typically JSON). Omit for GET/DELETE/HEAD requests. Mutually exclusive with form_file.",
				},
				"form_file": {
					Type:        "object",
					Description: "Send a multipart/form-data body with a file part streamed from a remote host at execution time — the bytes never transit the agent context. Keys: source_path (required, absolute path), source_host OR source_ctid (default: Proxmox host), field (form field name, default \"file\"), filename (default: source basename). POST/PUT/PATCH only; mutually exclusive with body.",
				},
				"form_fields": {
					Type:        "object",
					Description: "Extra string fields for the multipart body (requires form_file), e.g. {\"name\": \"report.xlsx\"}.",
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
		Name:        "create_then_run",
		Description: "Create a oneshot script and run it in a single approval. The default target is /opt/human-relay/scripts/oneshot/<name>.{sh,py,json} (extension auto-detected from content, same rules as create_script). If <name> contains a slash it is used as-is (no oneshot/ prefix). Refuses if a file already exists at any extension of the target path. After the run, the script persists on disk and can be re-run via run_script(name=\"oneshot/<name>\"). Use this for fire-once automation where a separate create_script + run_script round-trip would waste an approval.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"name": {
					Type:        "string",
					Description: "Script name. Alphanumeric segments (hyphens/underscores allowed) joined by single slashes. No leading/trailing/double slashes, no '.' or '..' segments. Plain names default to oneshot/<name>.",
				},
				"content": {
					Type:        "string",
					Description: "The full script content. JSON objects saved as .json pipelines, scripts starting with #!/bin/bash or #!/bin/sh as .sh, everything else as .py.",
				},
				"args": {
					Type:        "array",
					Description: "Arguments passed to the script when it runs. Same shape as run_script args.",
					Items:       &Items{Type: "string"},
				},
				"reason": {
					Type:        "string",
					Description: "Why this script is being created and run (shown to the human reviewer alongside the script content)",
				},
				"timeout": {
					Type:        "integer",
					Description: "Script timeout in seconds (default: server default, max: server max)",
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
					Description: "Target container ID (e.g. 125). Provide this OR machine.",
				},
				"machine": {
					Type:        "string",
					Description: "Target machine name (non-LXC SSH target). Provide this OR ctid. Appends to the login user's authorized_keys (~/.ssh on posix, %USERPROFILE%\\.ssh on powershell).",
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
			Required: []string{"public_key", "reason"},
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

// WriteFileChecker probes a target file for the write_file pre-approval
// overwrite warning. Returns (exists, size, mtime, err). On any error
// (SSH failure, permission denied, timeout) callers MUST fail-open: proceed
// with the write and omit the warning from the approval reason. A clean
// "file does not exist" result is (false, 0, zero-time, nil) — no error.
type WriteFileChecker func(ctid int, host, path string, timeout time.Duration) (exists bool, size int64, mtime time.Time, err error)

type ToolHandler struct {
	store          *store.Store
	containers     *containers.Store
	machines       *machines.Store
	hostIP         string
	audit          *audit.Logger
	sshConfigFile  string
	relayPubkeyFile string
	scriptsDir     string
	writeFileChecker WriteFileChecker
}

func NewToolHandler(s *store.Store, cs *containers.Store, ms *machines.Store, hostIP string, al *audit.Logger) *ToolHandler {
	h := &ToolHandler{store: s, containers: cs, machines: ms, hostIP: hostIP, audit: al, scriptsDir: "/scripts"}
	h.writeFileChecker = h.defaultWriteFileCheck
	return h
}

// SetWriteFileChecker overrides the write_file overwrite probe. Tests use this
// to stub without SSH; production uses the default SSH-based implementation.
func (h *ToolHandler) SetWriteFileChecker(c WriteFileChecker) {
	h.writeFileChecker = c
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
	case "register_machine":
		return h.registerMachine(args)
	case "list_machines":
		return h.listMachines(args)
	case "delete_machine":
		return h.deleteMachine(args)
	case "exec_machine":
		return h.execMachine(args)
	case "write_file":
		return h.writeFile(args)
	case "http_request":
		return h.httpRequest(args)
	case "run_script":
		return h.runScript(args)
	case "create_script":
		return h.createScript(args)
	case "create_then_run":
		return h.createThenRun(args)
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
	if r.OutputGated && r.Result != nil {
		gated := *r
		gr := *r.Result
		stdoutLen := len(gr.Stdout)
		stderrLen := len(gr.Stderr)
		gr.Stdout = fmt.Sprintf("[output gated by operator — %d bytes. use release button in dashboard to unlock, then re-poll get_result]", stdoutLen)
		if stderrLen > 0 {
			gr.Stderr = fmt.Sprintf("[stderr gated — %d bytes]", stderrLen)
		}
		gated.Result = &gr
		data, _ := json.Marshal(gated)
		return textResult(string(data))
	}
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

// validMachineNameRe matches a machine registry key: alphanumeric start,
// then alphanumeric/hyphen/underscore. Same shape as a flat script name.
var validMachineNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func (h *ToolHandler) registerMachine(args map[string]interface{}) *CallToolResult {
	if h.machines == nil {
		return errorResult("machine registry not configured")
	}
	name, _ := args["name"].(string)
	if name == "" {
		return errorResult("name is required")
	}
	if !validMachineNameRe.MatchString(name) {
		return errorResult("machine name must be alphanumeric with hyphens/underscores only (no paths, no spaces)")
	}
	host, _ := args["host"].(string)
	if host == "" {
		return errorResult("host is required")
	}
	if !validHostRe.MatchString(host) {
		return errorResult("host must be an IP or hostname")
	}
	sshUser, _ := args["ssh_user"].(string)
	if sshUser == "" {
		return errorResult("ssh_user is required")
	}
	shell, _ := args["shell"].(string)
	switch shell {
	case "", machines.ShellPosix, machines.ShellPowerShell:
	default:
		return errorResult(fmt.Sprintf("shell must be %q or %q", machines.ShellPosix, machines.ShellPowerShell))
	}
	identityFile, _ := args["identity_file"].(string)
	if identityFile != "" && !validPathRe.MatchString(identityFile) {
		return errorResult("identity_file must be an absolute path")
	}

	m, err := h.machines.Register(name, host, sshUser, shell, identityFile)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to register machine: %v", err))
	}
	data, _ := json.Marshal(m)
	return textResult(string(data))
}

func (h *ToolHandler) listMachines(args map[string]interface{}) *CallToolResult {
	if h.machines == nil {
		return errorResult("machine registry not configured")
	}
	list, err := h.machines.List()
	if err != nil {
		return errorResult(fmt.Sprintf("failed to list machines: %v", err))
	}
	if list == nil {
		list = []*machines.Machine{}
	}
	data, _ := json.Marshal(list)
	return textResult(string(data))
}

func (h *ToolHandler) deleteMachine(args map[string]interface{}) *CallToolResult {
	if h.machines == nil {
		return errorResult("machine registry not configured")
	}
	name, _ := args["name"].(string)
	if name == "" {
		return errorResult("name is required")
	}
	if err := h.machines.Delete(name); err != nil {
		return errorResult(fmt.Sprintf("failed to delete machine: %v", err))
	}
	result := map[string]interface{}{"name": name, "deleted": true}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}

// machineSSHBase returns the ssh arg prefix for a machine up to and including
// "user@host --". It applies the relay's -F config, an optional per-machine
// -i identity file, and the machine's login user (overriding the config's
// default User for Host *).
func (h *ToolHandler) machineSSHBase(m *machines.Machine) []string {
	args := append([]string{}, h.sshPrefix()...)
	if m.IdentityFile != "" {
		args = append(args, "-i", m.IdentityFile)
	}
	args = append(args, fmt.Sprintf("%s@%s", m.SSHUser, m.Host), "--")
	return args
}

func (h *ToolHandler) execMachine(args map[string]interface{}) *CallToolResult {
	if h.machines == nil {
		return errorResult("machine registry not configured")
	}
	name, _ := args["machine"].(string)
	if name == "" {
		return errorResult("machine is required")
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

	// bash/sh -c arg-splitting guard applies to posix machines in non-shell mode
	// (same rule as exec_container). PowerShell has no such failure mode.
	if !shell {
		if errMsg := checkBashCArgSplitting(command, cmdArgs); errMsg != "" {
			return errorResult(errMsg)
		}
	}

	m, err := h.machines.Get(name)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to look up machine: %v", err))
	}
	if m == nil {
		return errorResult(fmt.Sprintf("machine %q not found in registry. Use register_machine first.", name))
	}

	sshArgs := append(h.machineSSHBase(m), machineExecRemote(m, command, cmdArgs, shell)...)
	prefixedReason := fmt.Sprintf("[MACHINE %s (%s)] %s", m.Name, m.Shell, reason)

	r := h.store.Add("ssh", sshArgs, prefixedReason, "", false, timeout)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":    "exec_machine",
		"machine": m.Name,
		"host":    m.Host,
		"shell":   m.Shell,
		"command": command,
		"args":    cmdArgs,
		"reason":  reason,
		"timeout": timeout,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
		"machine":    m.Name,
		"shell":      m.Shell,
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

	// Multipart form support: form_file streams the file bytes from a remote
	// host at execution time (fetch_cmd runs on the relay), form_fields adds
	// plain string parts. Bytes never transit the agent context.
	var formFile *store.FormFile
	if raw, present := args["form_file"]; present && raw != nil {
		rawFF, ok := raw.(map[string]interface{})
		if !ok {
			return errorResult(fmt.Sprintf("form_file must be a JSON object, got %T — if the relay was just redeployed, reconnect the MCP session to refresh tool schemas", raw))
		}
		if body != "" {
			return errorResult("form_file and body are mutually exclusive")
		}
		switch method {
		case "POST", "PUT", "PATCH":
		default:
			return errorResult("form_file requires POST, PUT, or PATCH")
		}
		var errRes *CallToolResult
		formFile, errRes = h.buildFormFile(rawFF)
		if errRes != nil {
			return errRes
		}
	}
	var formFields map[string]string
	if raw, present := args["form_fields"]; present && raw != nil {
		rawFields, ok := raw.(map[string]interface{})
		if !ok {
			return errorResult(fmt.Sprintf("form_fields must be a JSON object, got %T — if the relay was just redeployed, reconnect the MCP session to refresh tool schemas", raw))
		}
		if formFile == nil {
			return errorResult("form_fields requires form_file")
		}
		formFields = make(map[string]string)
		for k, v := range rawFields {
			if s, ok := v.(string); ok {
				formFields[k] = s
			}
		}
	}

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	r := h.store.AddHTTPForm(method, url, headers, body, formFile, formFields, reason, timeout)

	// Build a friendly display command
	displayCmd := fmt.Sprintf("%s %s", method, url)
	if formFile != nil {
		displayCmd = fmt.Sprintf("%s %s  [multipart file from %s]", method, url, formFile.Source)
	}
	h.store.SetDisplayCommand(r.ID, displayCmd)

	auditEntry := map[string]interface{}{
		"tool":    "http_request",
		"method":  method,
		"url":     url,
		"headers": headerKeys(headers),
		"has_body": body != "",
		"reason":  reason,
		"timeout": timeout,
	}
	if formFile != nil {
		auditEntry["form_file_source"] = formFile.Source
	}
	h.audit.Log("request_created", r.ID, auditEntry)

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}

// validScriptNameRe matches a single safe script name segment:
// alphanumeric, hyphens, underscores, must start with alphanumeric.
// Used by create_script (flat names only).
var validScriptNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// validScriptPathRe matches subpath script names: one or more segments
// (each matching validScriptNameRe) joined by single slashes.
// Rejects: leading/trailing/double slashes, `.` and `..` segments, absolute
// paths, anything with spaces or dots. Used by run_script and create_then_run.
var validScriptPathRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*(/[a-zA-Z0-9][a-zA-Z0-9_-]*)*$`)

func (h *ToolHandler) runScript(args map[string]interface{}) *CallToolResult {
	name, _ := args["name"].(string)
	reason, _ := args["reason"].(string)

	if name == "" || reason == "" {
		return errorResult("name and reason are required")
	}

	if !validScriptPathRe.MatchString(name) {
		return errorResult("script name must be alphanumeric segments (hyphens/underscores allowed) joined by single slashes; no path traversal, no leading/trailing slashes, no extensions")
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

// detectScriptExt returns ".json" for JSON objects, ".sh" for scripts with a
// supported bash/sh shebang, and ".py" for everything else. Mirrors the logic
// in executor/script.go#detectScriptType but returns the extension directly.
func detectScriptExt(content string) string {
	var obj map[string]interface{}
	if json.Unmarshal([]byte(content), &obj) == nil {
		return ".json"
	}
	if strings.HasPrefix(content, "#!/bin/bash") ||
		strings.HasPrefix(content, "#!/bin/sh") ||
		strings.HasPrefix(content, "#!/usr/bin/env bash") ||
		strings.HasPrefix(content, "#!/usr/bin/env sh") {
		return ".sh"
	}
	return ".py"
}

func (h *ToolHandler) createThenRun(args map[string]interface{}) *CallToolResult {
	name, _ := args["name"].(string)
	content, _ := args["content"].(string)
	reason, _ := args["reason"].(string)

	if name == "" || content == "" || reason == "" {
		return errorResult("name, content, and reason are required")
	}

	if !validScriptPathRe.MatchString(name) {
		return errorResult("script name must be alphanumeric segments (hyphens/underscores allowed) joined by single slashes; no path traversal, no leading/trailing slashes, no extensions")
	}

	// Plain names default to oneshot/<name>. A caller-supplied subpath is
	// used as-is (for experiments/ or other deliberate subdirs).
	targetName := name
	if !strings.Contains(name, "/") {
		targetName = "oneshot/" + name
	}

	ext := detectScriptExt(content)

	// Refuse if any extension of the target already exists. The cross-extension
	// check avoids the case where create_then_run(foo, <shell>) silently shadows
	// an existing oneshot/foo.py (since run_script picks .sh over .py).
	for _, e := range []string{".sh", ".py", ".json"} {
		p := fmt.Sprintf("%s/%s%s", h.scriptsDir, targetName, e)
		if _, err := os.Stat(p); err == nil {
			return errorResult(fmt.Sprintf("file already exists at %s; pick a different name", p))
		}
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

	// Build the approval reason. Shows target path, size, requested args, and
	// the full script content so the reviewer sees create + run in one view.
	preview := content
	if len(preview) > 4096 {
		preview = preview[:4096] + "\n... (truncated)"
	}
	argsStr := ""
	if len(scriptArgs) > 0 {
		argsStr = " args=[" + strings.Join(scriptArgs, " ") + "]"
	}
	prefixedReason := fmt.Sprintf("[CREATE+RUN %s%s %dB]%s %s\n---\n%s",
		targetName, ext, len(content), argsStr, reason, preview)

	r := h.store.AddScript(targetName, scriptArgs, prefixedReason, timeout)
	r.Type = "script_create_then_run"
	h.store.SetStdin(r.ID, []byte(content))

	displayCmd := fmt.Sprintf("create_then_run %s%s (%dB)", targetName, ext, len(content))
	if len(scriptArgs) > 0 {
		displayCmd += " " + strings.Join(scriptArgs, " ")
	}
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":    "create_then_run",
		"script":  targetName,
		"ext":     ext,
		"size":    len(content),
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

// headerKeys returns just the header names (not values) for audit logging,
// avoiding leaking auth tokens into the audit log.
func headerKeys(headers map[string]string) []string {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	return keys
}

// defaultWriteFileCheck runs `stat -c '%s %Y' <path>` on the target via SSH,
// mirroring the write's routing (direct SSH or pct exec). Returns
// (exists, size, mtime, err). Non-existent files return (false, 0, zero, nil);
// SSH errors / permission denied / timeout return err != nil so the caller
// can fail-open.
//
// The probe is best-effort by design: any stderr or non-zero exit means "we
// don't know," and callers omit the warning rather than guess. Only a clean
// exit with a parseable `<size> <unix-ts>` stdout produces a warning.
func (h *ToolHandler) defaultWriteFileCheck(ctid int, host, path string, timeout time.Duration) (bool, int64, time.Time, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	pfx := h.sshPrefix()
	var sshArgs []string

	if ctid > 0 {
		c, err := h.containers.Get(ctid)
		if err != nil || c == nil {
			return false, 0, time.Time{}, fmt.Errorf("probe: container %d not found: %v", ctid, err)
		}
		if c.HasRelaySSH {
			user := "root"
			if c.SSHUser != "" {
				user = c.SSHUser
			}
			// stat returns non-zero when the path doesn't exist. We want a
			// clean "doesn't exist" to come back as (false, 0, zero, nil),
			// so swallow stat's stderr and check stdout emptiness instead.
			sshArgs = append(pfx, fmt.Sprintf("%s@%s", user, c.IP), "--",
				fmt.Sprintf("stat -c '%%s %%Y' %s 2>/dev/null", shellQuote(path)))
		} else {
			sshArgs = append(pfx, fmt.Sprintf("root@%s", h.hostIP), "--",
				"pct", "exec", fmt.Sprintf("%d", c.CTID), "--",
				"sh", "-c", fmt.Sprintf("stat -c '%%s %%Y' %s 2>/dev/null", shellQuote(path)))
		}
	} else {
		target := host
		if target == "" {
			target = h.hostIP
		}
		sshArgs = append(pfx, fmt.Sprintf("root@%s", target), "--",
			fmt.Sprintf("stat -c '%%s %%Y' %s 2>/dev/null", shellQuote(path)))
	}

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return false, 0, time.Time{}, fmt.Errorf("probe timed out after %s", timeout)
	}
	if err != nil {
		// SSH exited non-zero. This covers connection refused, auth failures,
		// and pct-exec dispatch errors. We intentionally do NOT treat this
		// as "file doesn't exist" — we don't know, so fail-open.
		return false, 0, time.Time{}, fmt.Errorf("probe ssh: %w", err)
	}

	line := strings.TrimSpace(string(out))
	if line == "" {
		// Clean exit with empty stdout = file doesn't exist (stat wrote to
		// stderr which we swallowed).
		return false, 0, time.Time{}, nil
	}

	parts := strings.Fields(line)
	if len(parts) != 2 {
		return false, 0, time.Time{}, fmt.Errorf("probe: unexpected stat output %q", line)
	}
	size, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false, 0, time.Time{}, fmt.Errorf("probe: bad size %q: %w", parts[0], err)
	}
	ts, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false, 0, time.Time{}, fmt.Errorf("probe: bad mtime %q: %w", parts[1], err)
	}
	return true, size, time.Unix(ts, 0), nil
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

// intArgStrict is intArg with a hard error when the key is present but not a
// number. Catches the stale-MCP-schema failure mode where a client sends
// params its cached schema doesn't know as strings — silently treating those
// as absent produces confusing downstream behavior (observed 2026-06-05:
// source_ctid as a string made write_file default the source to the Proxmox
// host). Returns (0, nil) when absent.
func intArgStrict(args map[string]interface{}, key string) (int, *CallToolResult) {
	raw, present := args[key]
	if !present || raw == nil {
		return 0, nil
	}
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	}
	return 0, errorResult(fmt.Sprintf("%s must be a number, got %T — if the relay was just redeployed, reconnect the MCP session to refresh tool schemas", key, raw))
}

var validPathRe = regexp.MustCompile(`^/[a-zA-Z0-9._/\-]+$`)

// validMachinePathRe allows posix AND Windows-style absolute paths (drive
// letters, backslashes, colons, spaces) for machine write targets. Paths are
// quote-escaped before use (shellQuote for posix, pwshQuote inside an
// -EncodedCommand for powershell), so this is a sanity bound on the charset, not
// the injection defense. Shell metacharacters ($ ` " ' ; | & etc.) stay blocked.
var validMachinePathRe = regexp.MustCompile(`^[A-Za-z0-9 ._/\\:\-]+$`)

var validModeRe = regexp.MustCompile(`^0[0-7]{3}$`)

// validHostRe constrains hosts that get interpolated into shell pipelines
// (write_file source streaming). IPs or hostnames only — no shell metachars.
var validHostRe = regexp.MustCompile(`^[a-zA-Z0-9._\-]+$`)

// bashCShellRe matches "bash -c word word" or "sh -c word word" patterns in a
// concatenated shell command string. The first word after -c is an unquoted
// single-word command, followed by another word — indicating arg-splitting.
// Does NOT match when the command after -c starts with a quote (properly quoted).
var bashCShellRe = regexp.MustCompile(`\b(?:bash|sh)\s+-c\s+([^\s'"` + "`" + `]+)\s+\S+`)

// writeFileFromSource handles write_file with source_path: the relay pulls the
// file over SSH from the source and pipes it into the destination write as a
// single shell pipeline. No bytes transit the agent context or the request
// store. Both paths are validated by validPathRe and hosts by validHostRe, so
// shell interpolation is safe.
func (h *ToolHandler) writeFileFromSource(args map[string]interface{}, path, reason, sourcePath, sourceHost string, sourceCtid int) *CallToolResult {
	mode := "0644"
	if m, ok := args["mode"].(string); ok && m != "" {
		mode = m
	}
	if !validModeRe.MatchString(mode) {
		return errorResult("mode must be an octal permission string like 0644 or 0755")
	}

	ctid, typeErr := intArgStrict(args, "ctid")
	if typeErr != nil {
		return typeErr
	}
	host, _ := args["host"].(string)
	if host == "" {
		host = h.hostIP
	}
	if !validHostRe.MatchString(host) {
		return errorResult("host must be an IP or hostname")
	}

	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}

	sshBin := "ssh"
	if pfx := h.sshPrefix(); len(pfx) > 0 {
		sshBin = "ssh " + strings.Join(pfx, " ")
	}

	// Source side: direct SSH to a host/container, or pct exec via the
	// Proxmox host for containers without relay SSH.
	var srcCmd, srcTarget string
	probeHost := ""
	if sourceCtid > 0 {
		c, err := h.containers.Get(sourceCtid)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to look up source container: %v", err))
		}
		if c == nil {
			return errorResult(fmt.Sprintf("source container %d not found in registry. Use register_container first.", sourceCtid))
		}
		srcTarget = fmt.Sprintf("CTID %d (%s)", c.CTID, c.Hostname)
		if c.HasRelaySSH {
			u := "root"
			if c.SSHUser != "" {
				u = c.SSHUser
			}
			srcCmd = fmt.Sprintf("%s %s@%s -- cat %s", sshBin, u, c.IP, shellQuote(sourcePath))
		} else {
			srcCmd = fmt.Sprintf("%s root@%s -- pct exec %d -- cat %s", sshBin, h.hostIP, c.CTID, shellQuote(sourcePath))
		}
	} else {
		if sourceHost == "" {
			sourceHost = h.hostIP
		}
		if !validHostRe.MatchString(sourceHost) {
			return errorResult("source_host must be an IP or hostname")
		}
		srcTarget = sourceHost
		probeHost = sourceHost
		srcCmd = fmt.Sprintf("%s root@%s -- cat %s", sshBin, sourceHost, shellQuote(sourcePath))
	}

	// Destination side mirrors the inline-content routing, minus stdin. The
	// remote command is double-quoted so the relay-side shell hands it to ssh
	// as one argument; inner single quotes come from shellQuote.
	var dstCmd, dstTarget, route string
	if ctid > 0 {
		c, err := h.containers.Get(ctid)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to look up container: %v", err))
		}
		if c == nil {
			return errorResult(fmt.Sprintf("container %d not found in registry. Use register_container first.", ctid))
		}
		dstTarget = fmt.Sprintf("CTID %d (%s)", c.CTID, c.Hostname)
		if c.HasRelaySSH {
			route = "direct_ssh"
			u := "root"
			if c.SSHUser != "" {
				u = c.SSHUser
			}
			dstCmd = fmt.Sprintf(`%s %s@%s -- "cat > %s && chmod %s %s"`,
				sshBin, u, c.IP, shellQuote(path), mode, shellQuote(path))
		} else {
			route = "pct_push"
			tmpFile := fmt.Sprintf("/tmp/mhr-%d", time.Now().UnixNano())
			dstCmd = fmt.Sprintf(`%s root@%s -- "cat > %s && pct push %d %s %s && pct exec %d -- chmod %s %s && rm %s"`,
				sshBin, h.hostIP, tmpFile, c.CTID, tmpFile, shellQuote(path), c.CTID, mode, shellQuote(path), tmpFile)
		}
	} else {
		route = "direct_ssh"
		dstTarget = host
		dstCmd = fmt.Sprintf(`%s root@%s -- "cat > %s && chmod %s %s"`,
			sshBin, host, shellQuote(path), mode, shellQuote(path))
	}

	// Pre-approval source probe: size + mtime for the reviewer, and a hard
	// error if the source file definitively does not exist. Fail-open on
	// probe errors (SSH failure, timeout) — same policy as the overwrite probe.
	sourceLine := ""
	sourceSize := 0
	if h.writeFileChecker != nil {
		exists, size, mtime, probeErr := h.writeFileChecker(sourceCtid, probeHost, sourcePath, 3*time.Second)
		if probeErr != nil {
			h.audit.Log("write_file_source_probe_failed", "", map[string]interface{}{
				"source": srcTarget,
				"path":   sourcePath,
				"error":  probeErr.Error(),
			})
		} else if !exists {
			return errorResult(fmt.Sprintf("source file not found: %s on %s", sourcePath, srcTarget))
		} else {
			sourceLine = fmt.Sprintf("[SOURCE: %dB, modified %s]\n",
				size, mtime.Format("2006-01-02 15:04"))
			sourceSize = int(size)
		}
	}

	// Overwrite probe on the destination, same as the inline path.
	overwriteLine := ""
	if h.writeFileChecker != nil {
		exists, oldSize, oldMtime, probeErr := h.writeFileChecker(ctid, host, path, 3*time.Second)
		if probeErr != nil {
			h.audit.Log("write_file_probe_failed", "", map[string]interface{}{
				"target": dstTarget,
				"path":   path,
				"error":  probeErr.Error(),
			})
		} else if exists {
			overwriteLine = fmt.Sprintf("[OVERWRITE: %dB, modified %s]\n",
				oldSize, oldMtime.Format("2006-01-02 15:04"))
		}
	}

	full := srcCmd + " | " + dstCmd
	prefixedReason := fmt.Sprintf("[FILE from %s:%s -> %s:%s] %s\n%s%s",
		srcTarget, sourcePath, dstTarget, path, reason, sourceLine, overwriteLine)

	r := h.store.Add(full, nil, prefixedReason, "", true, timeout)

	displayCmd := fmt.Sprintf("stream %s:%s -> %s:%s  [mode %s]",
		srcTarget, sourcePath, dstTarget, path, mode)
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":        "write_file",
		"target":      dstTarget,
		"path":        path,
		"source":      srcTarget,
		"source_path": sourcePath,
		"size":        sourceSize,
		"mode":        mode,
		"route":       route,
		"reason":      reason,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
		"target":     dstTarget,
		"path":       path,
		"source":     fmt.Sprintf("%s:%s", srcTarget, sourcePath),
		"size":       sourceSize,
		"route":      route,
	}
	data, _ := json.Marshal(result)
	return textResult(string(data))
}

// buildFormFile validates http_request's form_file argument and constructs
// the store.FormFile with its fetch command. The fetch command is an argv
// array (no shell), so source_path needs no quoting beyond validPathRe.
func (h *ToolHandler) buildFormFile(raw map[string]interface{}) (*store.FormFile, *CallToolResult) {
	srcPath, _ := raw["source_path"].(string)
	if srcPath == "" {
		return nil, errorResult("form_file.source_path is required")
	}
	if !validPathRe.MatchString(srcPath) {
		return nil, errorResult("form_file.source_path must be absolute with only alphanumeric, dot, dash, underscore, and slash characters")
	}
	srcHost, _ := raw["source_host"].(string)
	srcCtid, typeErr := intArgStrict(raw, "source_ctid")
	if typeErr != nil {
		return nil, typeErr
	}
	if srcHost != "" && srcCtid > 0 {
		return nil, errorResult("form_file: provide either source_host or source_ctid, not both")
	}

	pfx := h.sshPrefix()
	var fetchCmd []string
	var source string
	if srcCtid > 0 {
		c, err := h.containers.Get(srcCtid)
		if err != nil {
			return nil, errorResult(fmt.Sprintf("failed to look up source container: %v", err))
		}
		if c == nil {
			return nil, errorResult(fmt.Sprintf("source container %d not found in registry. Use register_container first.", srcCtid))
		}
		source = fmt.Sprintf("CTID %d (%s):%s", c.CTID, c.Hostname, srcPath)
		if c.HasRelaySSH {
			u := "root"
			if c.SSHUser != "" {
				u = c.SSHUser
			}
			fetchCmd = append(append([]string{"ssh"}, pfx...),
				fmt.Sprintf("%s@%s", u, c.IP), "--", "cat", srcPath)
		} else {
			fetchCmd = append(append([]string{"ssh"}, pfx...),
				fmt.Sprintf("root@%s", h.hostIP), "--",
				"pct", "exec", strconv.Itoa(c.CTID), "--", "cat", srcPath)
		}
	} else {
		if srcHost == "" {
			srcHost = h.hostIP
		}
		if !validHostRe.MatchString(srcHost) {
			return nil, errorResult("form_file.source_host must be an IP or hostname")
		}
		source = fmt.Sprintf("%s:%s", srcHost, srcPath)
		fetchCmd = append(append([]string{"ssh"}, pfx...),
			fmt.Sprintf("root@%s", srcHost), "--", "cat", srcPath)
	}

	field, _ := raw["field"].(string)
	if field == "" {
		field = "file"
	}
	filename, _ := raw["filename"].(string)
	if filename == "" {
		parts := strings.Split(srcPath, "/")
		filename = parts[len(parts)-1]
	}

	return &store.FormFile{
		Field:    field,
		Filename: filename,
		FetchCmd: fetchCmd,
		Source:   source,
	}, nil
}

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

	// Machine targets allow Windows-style paths; container/host targets keep the
	// stricter posix-absolute rule.
	if _, isMachine := args["machine"].(string); isMachine && args["machine"].(string) != "" {
		if !validMachinePathRe.MatchString(path) {
			return errorResult("path contains disallowed characters (allowed: letters, digits, space, . _ - / \\ :)")
		}
	} else if !validPathRe.MatchString(path) {
		return errorResult("path must be absolute with only alphanumeric, dot, dash, underscore, and slash characters")
	}

	// Content can arrive as plain text (`content`), base64 (`content_base64`),
	// or be streamed from a remote host (`source_path` + `source_host`/`source_ctid`).
	// Exactly one content method is required.
	plaintext, hasPlain := args["content"].(string)
	contentB64, hasB64 := args["content_base64"].(string)
	sourcePath, _ := args["source_path"].(string)
	sourceHost, _ := args["source_host"].(string)
	sourceCtid, typeErr := intArgStrict(args, "source_ctid")
	if typeErr != nil {
		return typeErr
	}

	if sourcePath != "" && ((hasPlain && plaintext != "") || (hasB64 && contentB64 != "")) {
		return errorResult("provide exactly one of content, content_base64, or source_path")
	}
	if sourcePath == "" && (sourceHost != "" || sourceCtid > 0) {
		return errorResult("source_host/source_ctid require source_path")
	}
	if sourcePath != "" {
		if sourceHost != "" && sourceCtid > 0 {
			return errorResult("provide either source_host or source_ctid, not both")
		}
		if !validPathRe.MatchString(sourcePath) {
			return errorResult("source_path must be absolute with only alphanumeric, dot, dash, underscore, and slash characters")
		}
		return h.writeFileFromSource(args, path, reason, sourcePath, sourceHost, sourceCtid)
	}

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

	ctid, typeErr := intArgStrict(args, "ctid")
	if typeErr != nil {
		return typeErr
	}
	machineName, _ := args["machine"].(string)
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
	// stdinBytes is what gets piped to the remote command. posix targets receive
	// the raw content; a powershell machine receives base64 that its remote script
	// decodes (set in the machine branch below).
	stdinBytes := content

	if machineName != "" {
		if h.machines == nil {
			return errorResult("machine registry not configured")
		}
		m, err := h.machines.Get(machineName)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to look up machine: %v", err))
		}
		if m == nil {
			return errorResult(fmt.Sprintf("machine %q not found in registry. Use register_machine first.", machineName))
		}
		target = fmt.Sprintf("machine %s (%s)", m.Name, m.Shell)
		route = "machine_" + m.Shell
		var remote []string
		remote, stdinBytes = machineWriteRemote(m, path, mode, content)
		sshArgs = append(h.machineSSHBase(m), remote...)
	} else if ctid > 0 {
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

	// Pre-approval overwrite probe. If the target file already exists, prepend
	// an [OVERWRITE: <size>B, modified <mtime>] marker to the approval reason so
	// the reviewer sees it before deciding. Fail-open: a probe error (SSH
	// failure, permission denied, timeout) must NOT block the write; log to
	// audit and proceed without the warning.
	// The overwrite probe SSHes as root@host or via pct exec — neither matches a
	// machine's login user/shell, so skip it for machine writes (fail-open: the
	// probe is reviewer-helpful signal, not a correctness gate).
	overwriteLine := ""
	if h.writeFileChecker != nil && machineName == "" {
		exists, oldSize, oldMtime, probeErr := h.writeFileChecker(ctid, host, path, 3*time.Second)
		if probeErr != nil {
			h.audit.Log("write_file_probe_failed", "", map[string]interface{}{
				"target": target,
				"path":   path,
				"error":  probeErr.Error(),
			})
		} else if exists {
			overwriteLine = fmt.Sprintf("[OVERWRITE: %dB, modified %s]\n",
				oldSize, oldMtime.Format("2006-01-02 15:04"))
		}
	}

	// Build content preview for the human reviewer
	preview := string(content)
	if len(preview) > 2048 {
		preview = preview[:2048] + "\n... (truncated)"
	}
	prefixedReason := fmt.Sprintf("[FILE %dB -> %s:%s] %s\n%s---\n%s",
		len(content), target, path, reason, overwriteLine, preview)

	r := h.store.AddWithStdin("ssh", sshArgs, prefixedReason, "", false, timeout, stdinBytes)

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
	machineName, _ := args["machine"].(string)
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

	if machineName != "" {
		return h.installSSHKeyMachine(machineName, pubkey, reason, timeout)
	}
	if ctid == 0 {
		return errorResult("ctid or machine is required")
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

// installSSHKeyMachine appends an arbitrary public key to a machine's
// authorized_keys over direct SSH (the relay must already have access). The key
// flows via stdin; the remote target path is the login user's profile.
func (h *ToolHandler) installSSHKeyMachine(name, pubkey, reason string, timeout int) *CallToolResult {
	if h.machines == nil {
		return errorResult("machine registry not configured")
	}
	m, err := h.machines.Get(name)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to look up machine: %v", err))
	}
	if m == nil {
		return errorResult(fmt.Sprintf("machine %q not found in registry. Use register_machine first.", name))
	}

	remote, stdin := machineKeyAppendRemote(m, pubkey)
	sshArgs := append(h.machineSSHBase(m), remote...)

	prefixedReason := fmt.Sprintf("[SSH KEY -> machine %s (%s)] %s\n---\nKey: %s", m.Name, m.Shell, reason, pubkey)
	r := h.store.AddWithStdin("ssh", sshArgs, prefixedReason, "", false, timeout, stdin)

	displayCmd := fmt.Sprintf("install SSH key -> machine %s", m.Name)
	h.store.SetDisplayCommand(r.ID, displayCmd)

	h.audit.Log("request_created", r.ID, map[string]interface{}{
		"tool":    "install_ssh_key",
		"machine": m.Name,
		"shell":   m.Shell,
		"key":     pubkey,
		"reason":  reason,
		"route":   "machine_" + m.Shell,
	})

	result := map[string]interface{}{
		"request_id": r.ID,
		"status":     "pending",
		"machine":    m.Name,
		"route":      "machine_" + m.Shell,
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
