// Package api is a typed client for the relay's web API: the request list and
// its cooldown/turbo headers, decisions, whitelist, turbocharge and the
// /events stream. It never sets an Origin header, never follows redirects and
// never reads proxy environment variables, because it carries a bearer token.
package api

import (
	"encoding/json"
	"time"
)

// Request statuses as the relay reports them.
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusDenied    = "denied"
	StatusRunning   = "running"
	StatusComplete  = "complete"
	StatusTimeout   = "timeout"
	StatusError     = "error"
	StatusWithdrawn = "withdrawn"
)

// Request types. A plain command has the empty type.
const (
	TypeCommand         = ""
	TypeHTTP            = "http"
	TypeScript          = "script"
	TypeScriptCreate    = "script_create"
	TypeScriptCreateRun = "script_create_then_run"
	TypeRegistryOp      = "registry_op"
)

// Result is what a finished request produced.
type Result struct {
	ExitCode        int               `json:"exit_code"`
	Stdout          string            `json:"stdout"`
	Stderr          string            `json:"stderr"`
	StatusCode      int               `json:"status_code,omitempty"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

// FormFile describes the file part of a multipart http request.
type FormFile struct {
	Field    string   `json:"field"`
	Filename string   `json:"filename"`
	FetchCmd []string `json:"fetch_cmd"`
	Source   string   `json:"source"`
}

// Request is one entry of GET /api/requests. Raw keeps the request's complete
// JSON object, so checks that must cover every field (including fields this
// struct does not know about) can walk it.
type Request struct {
	ID             string            `json:"id"`
	Type           string            `json:"type"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Reason         string            `json:"reason"`
	Client         string            `json:"client,omitempty"`
	WorkingDir     string            `json:"working_dir,omitempty"`
	Shell          bool              `json:"shell"`
	Timeout        int               `json:"timeout"`
	Status         string            `json:"status"`
	CreatedAt      time.Time         `json:"created_at"`
	DecidedAt      *time.Time        `json:"decided_at,omitempty"`
	DenyReason     string            `json:"deny_reason,omitempty"`
	WithdrawReason string            `json:"withdraw_reason,omitempty"`
	Result         *Result           `json:"result,omitempty"`
	StdinLen       int               `json:"stdin_len,omitempty"`
	StdinSHA256    string            `json:"stdin_sha256,omitempty"`
	DisplayCommand string            `json:"display_command,omitempty"`
	OutputGated    bool              `json:"output_gated,omitempty"`
	HTTPMethod     string            `json:"http_method,omitempty"`
	HTTPURL        string            `json:"http_url,omitempty"`
	HTTPHeaders    map[string]string `json:"http_headers,omitempty"`
	HTTPBody       string            `json:"http_body,omitempty"`
	HTTPFormFile   *FormFile         `json:"http_form_file,omitempty"`
	HTTPFormFields map[string]string `json:"http_form_fields,omitempty"`
	ScriptName     string            `json:"script_name,omitempty"`
	ScriptArgs     []string          `json:"script_args,omitempty"`
	RegistryOp     string            `json:"registry_op,omitempty"`
	RegistryArgs   map[string]string `json:"registry_args,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// Rule is one whitelist entry.
type Rule struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Listing is the result of GET /api/requests: the requests plus the cooldown
// and turbo state the relay reports in response headers.
type Listing struct {
	Requests          []Request
	CooldownRemaining time.Duration
	CooldownDuration  time.Duration
	TurboActive       bool
	TurboRemaining    time.Duration
}

// TurboState is GET /api/turbocharge.
type TurboState struct {
	Active    bool
	Remaining time.Duration
	Cooldown  time.Duration
}

// Content is the exact stdin a request will run against.
type Content struct {
	Body   []byte
	SHA256 string
}

// Frame is one /events message. Only Type and RequestID are always present.
type Frame struct {
	Type        string `json:"type"`
	RequestID   string `json:"request_id"`
	Status      string `json:"status,omitempty"`
	Client      string `json:"client,omitempty"`
	OutputGated bool   `json:"output_gated,omitempty"`
}
