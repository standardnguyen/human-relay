package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sync"
	"time"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusApproved  Status = "approved"
	StatusDenied    Status = "denied"
	StatusRunning   Status = "running"
	StatusComplete  Status = "complete"
	StatusTimeout   Status = "timeout"
	StatusError     Status = "error"
	StatusWithdrawn Status = "withdrawn"
)

type Request struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"` // "command" (default), "http", or "script"
	Command    string    `json:"command"`
	Args       []string  `json:"args"`
	Reason     string    `json:"reason"`
	Client     string    `json:"client,omitempty"`
	WorkingDir string    `json:"working_dir,omitempty"`
	Shell      bool      `json:"shell"`
	Timeout    int       `json:"timeout"`
	Status     Status    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	DecidedAt  *time.Time `json:"decided_at,omitempty"`
	DenyReason     string    `json:"deny_reason,omitempty"`
	WithdrawReason string    `json:"withdraw_reason,omitempty"`
	Result         *Result   `json:"result,omitempty"`
	Stdin          []byte     `json:"-"`
	StdinLen       int        `json:"stdin_len,omitempty"`
	// StdinSHA256 identifies the stdin bytes without exposing them. Script
	// whitelist rules key on it, so the dashboard needs it to tell whether a
	// given create_script body is already whitelisted.
	StdinSHA256    string     `json:"stdin_sha256,omitempty"`
	DisplayCommand string     `json:"display_command,omitempty"`
	OutputGated    bool       `json:"output_gated,omitempty"`

	// HTTP-specific fields (only when Type == "http")
	HTTPMethod  string            `json:"http_method,omitempty"`
	HTTPURL     string            `json:"http_url,omitempty"`
	HTTPHeaders map[string]string `json:"http_headers,omitempty"`
	HTTPBody    string            `json:"http_body,omitempty"`
	// Multipart upload: the executor runs FetchCmd to pull the file bytes at
	// execution time and builds a multipart/form-data body. Bytes never
	// transit the agent context.
	HTTPFormFile   *FormFile         `json:"http_form_file,omitempty"`
	HTTPFormFields map[string]string `json:"http_form_fields,omitempty"`

	// Script-specific fields (only when Type == "script")
	ScriptName string   `json:"script_name,omitempty"`
	ScriptArgs []string `json:"script_args,omitempty"`

	// Registry-specific fields (only when Type == "registry_op")
	// RegistryOp is one of "register_container", "delete_container",
	// "register_machine", "delete_machine"; RegistryArgs carries that op's
	// already-validated arguments, stringified (ints via strconv.Itoa,
	// bools as "true"/"false").
	RegistryOp   string            `json:"registry_op,omitempty"`
	RegistryArgs map[string]string `json:"registry_args,omitempty"`
}

// FormFile describes the file part of a multipart http_request. FetchCmd is
// built at request time (so the approval pane shows exactly what will run)
// and executed by the relay at execution time; its stdout becomes the file
// bytes. Source is a human-readable descriptor like "CTID 115:/shared/x.xlsx".
type FormFile struct {
	Field    string   `json:"field"`
	Filename string   `json:"filename"`
	FetchCmd []string `json:"fetch_cmd"`
	Source   string   `json:"source"`
}

type Result struct {
	ExitCode    int               `json:"exit_code"`
	Stdout      string            `json:"stdout"`
	Stderr      string            `json:"stderr"`
	StatusCode  int               `json:"status_code,omitempty"`
	RespHeaders map[string]string `json:"response_headers,omitempty"`
}

type Store struct {
	mu       sync.RWMutex
	requests map[string]*Request
	order    []string // insertion order
	notify   chan string // fires on new request
	updates  chan string // fires on status change (approve/deny/withdraw/etc)
}

func New() *Store {
	return &Store{
		requests: make(map[string]*Request),
		notify:   make(chan string, 100),
		updates:  make(chan string, 100),
	}
}

func (s *Store) Add(cmd string, args []string, reason, workingDir string, shell bool, timeout int, client string) *Request {
	id := generateID()
	r := &Request{
		ID:         id,
		Command:    cmd,
		Args:       args,
		Reason:     reason,
		Client:     client,
		WorkingDir: workingDir,
		Shell:      shell,
		Timeout:    timeout,
		Status:     StatusPending,
		CreatedAt:  time.Now(),
	}
	s.mu.Lock()
	s.requests[id] = r
	s.order = append(s.order, id)
	s.mu.Unlock()

	// Non-blocking send to notify listeners
	select {
	case s.notify <- id:
	default:
	}

	return r
}

func (s *Store) AddHTTP(method, url string, headers map[string]string, body, reason string, timeout int, client string) *Request {
	return s.AddHTTPForm(method, url, headers, body, nil, nil, reason, timeout, client)
}

func (s *Store) AddHTTPForm(method, url string, headers map[string]string, body string, formFile *FormFile, formFields map[string]string, reason string, timeout int, client string) *Request {
	id := generateID()
	r := &Request{
		ID:             id,
		Type:           "http",
		HTTPMethod:     method,
		HTTPURL:        url,
		HTTPHeaders:    headers,
		HTTPBody:       body,
		HTTPFormFile:   formFile,
		HTTPFormFields: formFields,
		Reason:         reason,
		Client:         client,
		Timeout:        timeout,
		Status:         StatusPending,
		CreatedAt:      time.Now(),
	}
	s.mu.Lock()
	s.requests[id] = r
	s.order = append(s.order, id)
	s.mu.Unlock()

	select {
	case s.notify <- id:
	default:
	}

	return r
}

// AddScript queues a plain run_script request (Type "script").
func (s *Store) AddScript(name string, args []string, reason string, timeout int, client string) *Request {
	return s.AddScriptTyped("script", name, args, reason, timeout, nil, client)
}

// AddScriptTyped queues a script-family request with an explicit Type
// ("script", "script_create", "script_create_then_run"). Type must be set at
// construction: callers must never mutate it on the returned pointer, because
// that pointer is already published into the store's map and readers (Get,
// List) touch it under the lock only.
//
// stdin (the script body, for the create types) is a construction argument for
// the same reason plus one more: publishing the request wakes the whitelist
// matcher, which keys script creates on a hash of this body. Filling it in
// after the fact would let the matcher see an empty body and mis-key the rule.
func (s *Store) AddScriptTyped(typ, name string, args []string, reason string, timeout int, stdin []byte, client string) *Request {
	id := generateID()
	r := &Request{
		ID:          id,
		Type:        typ,
		ScriptName:  name,
		ScriptArgs:  args,
		Reason:      reason,
		Client:      client,
		Timeout:     timeout,
		Status:      StatusPending,
		CreatedAt:   time.Now(),
		Stdin:       stdin,
		StdinLen:    len(stdin),
		StdinSHA256: StdinDigest(stdin),
	}
	s.mu.Lock()
	s.requests[id] = r
	s.order = append(s.order, id)
	s.mu.Unlock()

	select {
	case s.notify <- id:
	default:
	}

	return r
}

// AddRegistryOp creates a pending request for a container/machine registry
// mutation (register_container, delete_container, register_machine,
// delete_machine). The MCP tools validate the arguments and queue the request;
// the registry is only touched once a human approves in the dashboard, at
// which point the web handler applies the op.
func (s *Store) AddRegistryOp(op string, args map[string]string, reason string, client string) *Request {
	id := generateID()
	r := &Request{
		ID:           id,
		Type:         "registry_op",
		RegistryOp:   op,
		RegistryArgs: args,
		Reason:       reason,
		Client:       client,
		Status:       StatusPending,
		CreatedAt:    time.Now(),
	}
	s.mu.Lock()
	s.requests[id] = r
	s.order = append(s.order, id)
	s.mu.Unlock()

	select {
	case s.notify <- id:
	default:
	}

	return r
}

func (s *Store) AddWithStdin(cmd string, args []string, reason, workingDir string, shell bool, timeout int, stdin []byte, client string) *Request {
	id := generateID()
	r := &Request{
		ID:         id,
		Command:    cmd,
		Args:       args,
		Reason:     reason,
		Client:     client,
		WorkingDir: workingDir,
		Shell:      shell,
		Timeout:    timeout,
		Status:     StatusPending,
		CreatedAt:  time.Now(),
		Stdin:       stdin,
		StdinLen:    len(stdin),
		StdinSHA256: StdinDigest(stdin),
	}
	s.mu.Lock()
	s.requests[id] = r
	s.order = append(s.order, id)
	s.mu.Unlock()

	select {
	case s.notify <- id:
	default:
	}

	return r
}

func (s *Store) Get(id string) *Request {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.requests[id]
	if !ok {
		return nil
	}
	// Return a copy
	cp := *r
	if r.Result != nil {
		rc := *r.Result
		cp.Result = &rc
	}
	return &cp
}

func (s *Store) SetStatus(id string, status Status) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false
	}
	r.Status = status
	now := time.Now()
	if status == StatusApproved || status == StatusDenied {
		r.DecidedAt = &now
	}
	return true
}

func (s *Store) Deny(id string, reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false
	}
	r.Status = StatusDenied
	r.DenyReason = reason
	now := time.Now()
	r.DecidedAt = &now
	return true
}

// Withdraw transitions a pending request to StatusWithdrawn. Returns
// (ok, currentStatus). ok is false and currentStatus is StatusPending when
// the request does not exist; ok is false and currentStatus reflects the
// actual status when the request exists but is not pending.
func (s *Store) Withdraw(id string, reason string) (bool, Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false, StatusPending
	}
	if r.Status != StatusPending {
		return false, r.Status
	}
	r.Status = StatusWithdrawn
	r.WithdrawReason = reason
	now := time.Now()
	r.DecidedAt = &now

	select {
	case s.updates <- id:
	default:
	}

	return true, StatusWithdrawn
}

// Approve atomically transitions a pending request to StatusApproved, marking
// its output gated when gateOutput is set. The lookup, the pending check and
// the mutation all happen under one lock acquisition, so concurrent approvals
// of the same request produce exactly one ok=true -- a caller that gets
// ok=false must not execute the request.
//
// Returns (false, nil) when the request does not exist or is no longer pending.
// On success the returned request is a copy (same semantics as Get), not a live
// pointer into the store.
func (s *Store) Approve(id string, gateOutput bool) (bool, *Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false, nil
	}
	if r.Status != StatusPending {
		return false, nil
	}
	r.Status = StatusApproved
	now := time.Now()
	r.DecidedAt = &now
	if gateOutput {
		r.OutputGated = true
	}
	cp := *r
	if r.Result != nil {
		rc := *r.Result
		cp.Result = &rc
	}
	return true, &cp
}

// SetResult records a finished request's result and status. Gated output with
// nothing to withhold (no stdout, no stderr, no response headers: see
// hasGatedContent) is released in this same update: gating protects content
// and such a result has none, and releasing it in a
// second update published the request as finished and gated in between, so a
// reader waited for a Release click that was never needed. releasedEmpty
// reports that this update released it; ok is false when id is unknown.
func (s *Store) SetResult(id string, result *Result, status Status) (ok, releasedEmpty bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, found := s.requests[id]
	if !found {
		return false, false
	}
	r.Result = result
	r.Status = status
	if r.OutputGated && result != nil && !result.hasGatedContent() {
		r.OutputGated = false
		releasedEmpty = true
	}
	return true, releasedEmpty
}

func (s *Store) List(filter Status) []*Request {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Request
	for _, id := range s.order {
		r := s.requests[id]
		if filter == "" || r.Status == filter {
			cp := *r
			if r.Result != nil {
				rc := *r.Result
				cp.Result = &rc
			}
			out = append(out, &cp)
		}
	}
	return out
}

// Subscribe returns a channel that receives request IDs when new requests are added.
func (s *Store) Subscribe() <-chan string {
	return s.notify
}

// SubscribeUpdates returns a channel that receives request IDs when an
// existing request changes status (approve/deny/withdraw/etc). Only Withdraw
// currently signals through this channel -- the web handler signals approve
// and deny directly via broadcastEvent since those mutations originate there.
func (s *Store) SubscribeUpdates() <-chan string {
	return s.updates
}

// StdinDigest is the canonical fingerprint of a request's stdin bytes: hex
// SHA-256, empty for empty input. Whitelist matching for script creates keys
// on it, so every producer of that key must go through this one function.
func StdinDigest(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Store) SetDisplayCommand(id string, cmd string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false
	}
	r.DisplayCommand = cmd
	return true
}

func (s *Store) SetOutputGated(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false
	}
	r.OutputGated = true
	return true
}

func (s *Store) ReleaseOutput(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return false
	}
	r.OutputGated = false

	select {
	case s.updates <- id:
	default:
	}

	return true
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// visibleWhenGated is the part of a result that gating never withholds: the
// exit code and the HTTP status code, so a caller still learns whether the
// command or call worked. Every other field is withheld while output is gated.
// RedactGatedOutput builds its copy from this and SetResult auto-releases only
// a result that holds nothing more, so what gating hides and what counts as
// nothing to hide cannot drift apart.
func (res *Result) visibleWhenGated() Result {
	return Result{ExitCode: res.ExitCode, StatusCode: res.StatusCode}
}

// hasGatedContent reports whether gating res withholds anything: whether any
// field outside visibleWhenGated is set. An HTTP response with no body still
// has its response headers, so it has gated content.
func (res *Result) hasGatedContent() bool {
	return !reflect.DeepEqual(*res, res.visibleWhenGated())
}

// RedactGatedOutput returns r untouched when its output is not gated (never
// gated, or released), and otherwise a copy whose result keeps only
// visibleWhenGated: stdout (an HTTP response body lands there) and stderr
// become placeholders giving their byte counts, and the response headers are
// dropped. The exit code and HTTP status code stay.
//
// Every path that hands a request to a caller who may not see gated output
// must pass it through here: MCP get_result and list_requests always, the web
// request list for any token but the approver's.
func RedactGatedOutput(r *Request) *Request {
	if r == nil || !r.OutputGated || r.Result == nil {
		return r
	}
	gated := *r
	gr := r.Result.visibleWhenGated()
	stdoutLen := len(r.Result.Stdout)
	stderrLen := len(r.Result.Stderr)
	gr.Stdout = fmt.Sprintf("[output gated by operator — %d bytes. use release button in dashboard to unlock, then re-poll get_result]", stdoutLen)
	if stderrLen > 0 {
		gr.Stderr = fmt.Sprintf("[stderr gated — %d bytes]", stderrLen)
	}
	gated.Result = &gr
	return &gated
}
