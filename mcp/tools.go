package mcp

import (
	"encoding/json"
	"fmt"
	"time"

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
}

type ToolHandler struct {
	store *store.Store
}

func NewToolHandler(s *store.Store) *ToolHandler {
	return &ToolHandler{store: s}
}

func (h *ToolHandler) Handle(name string, args map[string]interface{}) *CallToolResult {
	switch name {
	case "request_command":
		return h.requestCommand(args)
	case "get_result":
		return h.getResult(args)
	case "list_requests":
		return h.listRequests(args)
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
