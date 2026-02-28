package mcp

import (
	"encoding/json"
	"fmt"
	"time"

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
}

type ToolHandler struct {
	store      *store.Store
	containers *containers.Store
	hostIP     string
}

func NewToolHandler(s *store.Store, cs *containers.Store, hostIP string) *ToolHandler {
	return &ToolHandler{store: s, containers: cs, hostIP: hostIP}
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

	c, err := h.containers.Register(ctid, ip, hostname, hasRelaySSH)
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

	// Build the SSH command
	var sshArgs []string
	if c.HasRelaySSH {
		// Direct SSH to container
		sshArgs = append(sshArgs, fmt.Sprintf("root@%s", c.IP), "--")
		if shell {
			full := command
			for _, a := range cmdArgs {
				full += " " + a
			}
			sshArgs = append(sshArgs, "sh", "-c", full)
		} else {
			sshArgs = append(sshArgs, command)
			sshArgs = append(sshArgs, cmdArgs...)
		}
	} else {
		// Fallback: SSH to host, then pct exec
		sshArgs = append(sshArgs, fmt.Sprintf("root@%s", h.hostIP), "pct", "exec", fmt.Sprintf("%d", ctid), "--")
		if shell {
			full := command
			for _, a := range cmdArgs {
				full += " " + a
			}
			sshArgs = append(sshArgs, "sh", "-c", full)
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
