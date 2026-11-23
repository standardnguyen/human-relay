package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxListBytes    = 64 << 20
	maxContentBytes = 32 << 20
	maxErrBody      = 2048
	callTimeout     = 20 * time.Second
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Client talks to one relay with one bearer token.
type Client struct {
	base *url.URL
	hc   *http.Client // bounded calls
	sse  *http.Client // the event stream: no overall timeout

	mu   sync.RWMutex
	orig []byte // the slice handed in, zeroed by Wipe
	tok  []byte // orig without surrounding whitespace
}

// New builds a client. The token slice is kept, not copied, so Wipe zeroes the
// caller's own bytes. Surrounding whitespace is trimmed.
func New(baseURL string, token []byte) (*Client, error) {
	u, err := ParseBase(baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	c := &Client{base: u}
	c.hc = &http.Client{Transport: newTransport(10 * time.Second), Timeout: callTimeout, CheckRedirect: refuseRedirect}
	c.sse = &http.Client{Transport: newTransport(10 * time.Second), CheckRedirect: refuseRedirect}
	c.SetToken(token)
	return c, nil
}

// ParseBase checks a relay address and returns it as a URL: http or https, with
// a host.
func ParseBase(baseURL string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("bad relay URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("bad relay URL %q: want http://host:port", baseURL)
	}
	return u, nil
}

func newTransport(headerTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 nil, // the bearer must never be routed through an environment-configured proxy
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
	}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return errors.New("redirect refused")
}

// SetToken replaces the token and zeroes the previous one.
func (c *Client) SetToken(token []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	zero(c.orig)
	c.orig = token
	c.tok = bytes.TrimSpace(token)
}

// Wipe zeroes the token. Calls made afterwards fail with ErrNoToken.
func (c *Client) Wipe() {
	c.mu.Lock()
	defer c.mu.Unlock()
	zero(c.orig)
	c.orig, c.tok = nil, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func (c *Client) bearer() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.tok) == 0 {
		return "", ErrNoToken
	}
	return "Bearer " + string(c.tok), nil
}

func (c *Client) endpoint(p string) string {
	u := *c.base
	u.Path += p
	return u.String()
}

func checkID(id string) error {
	if !idPattern.MatchString(id) {
		return ErrBadID
	}
	return nil
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

// do sends one call and returns the answer when the status is 2xx, or an *Error.
func (c *Client) do(ctx context.Context, method, path string, payload any, limit int64) (*reply, error) {
	auth, err := c.bearer()
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json, text/plain")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return nil, httpError(resp, b)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, transportError(ctx, err)
	}
	if int64(len(b)) > limit {
		return nil, ErrTooLarge
	}
	return &reply{status: resp.StatusCode, header: resp.Header, body: b}, nil
}

func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %w", ErrUnreachable, err)
}

func httpError(resp *http.Response, body []byte) *Error {
	e := &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	if resp.StatusCode == 429 {
		var j struct {
			RemainingMs int64 `json:"remaining_ms"`
		}
		if json.Unmarshal(body, &j) == nil {
			e.RemainingMs = j.RemainingMs
		}
		if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
	}
	return e
}

func msHeader(h http.Header, name string) time.Duration {
	n, err := strconv.ParseInt(strings.TrimSpace(h.Get(name)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return time.Duration(n) * time.Millisecond
}

// List fetches every request plus the cooldown and turbo state.
func (c *Client) List(ctx context.Context) (*Listing, error) {
	r, err := c.do(ctx, http.MethodGet, "/api/requests", nil, maxListBytes)
	if err != nil {
		return nil, err
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(r.body, &raws); err != nil {
		return nil, fmt.Errorf("decode request list: %w", err)
	}
	l := &Listing{
		Requests:          make([]Request, 0, len(raws)),
		CooldownRemaining: msHeader(r.header, "X-Cooldown-Remaining-Ms"),
		CooldownDuration:  msHeader(r.header, "X-Cooldown-Duration-Ms"),
		TurboActive:       strings.EqualFold(strings.TrimSpace(r.header.Get("X-Turbo-Active")), "true"),
	}
	if l.TurboActive {
		l.TurboRemaining = msHeader(r.header, "X-Turbo-Remaining-Ms")
	}
	for _, raw := range raws {
		var q Request
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, fmt.Errorf("decode request: %w", err)
		}
		q.Raw = append(json.RawMessage(nil), raw...)
		l.Requests = append(l.Requests, q)
	}
	return l, nil
}

// Content fetches the exact stdin bytes of a request.
func (c *Client) Content(ctx context.Context, id string) (*Content, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	r, err := c.do(ctx, http.MethodGet, "/api/requests/"+id+"/content", nil, maxContentBytes)
	if err != nil {
		return nil, err
	}
	return &Content{Body: r.body, SHA256: r.header.Get("X-Stdin-SHA256")}, nil
}

func (c *Client) decide(ctx context.Context, id, action string, payload any) error {
	if err := checkID(id); err != nil {
		return err
	}
	_, err := c.do(ctx, http.MethodPost, "/api/requests/"+id+"/"+action, payload, 1<<20)
	return err
}

// Approve approves a pending request.
func (c *Client) Approve(ctx context.Context, id string) error {
	return c.decide(ctx, id, "approve", nil)
}

// ApproveGated approves and withholds the output from the agent until Release.
func (c *Client) ApproveGated(ctx context.Context, id string) error {
	return c.decide(ctx, id, "approve-gated", nil)
}

// Release unlocks the output of a gated request.
func (c *Client) Release(ctx context.Context, id string) error {
	return c.decide(ctx, id, "release", nil)
}

// Deny denies a pending request. An empty reason is sent as is; the relay then
// records its own default.
func (c *Client) Deny(ctx context.Context, id, reason string) error {
	return c.decide(ctx, id, "deny", map[string]string{"reason": reason})
}

// Whitelist lists the auto-approval rules.
func (c *Client) Whitelist(ctx context.Context) ([]Rule, error) {
	r, err := c.do(ctx, http.MethodGet, "/api/whitelist", nil, maxListBytes)
	if err != nil {
		return nil, err
	}
	var rules []Rule
	if err := json.Unmarshal(r.body, &rules); err != nil {
		return nil, fmt.Errorf("decode whitelist: %w", err)
	}
	return rules, nil
}

// AddWhitelist whitelists the key of an existing request; the relay derives
// the rule from the stored request itself.
func (c *Client) AddWhitelist(ctx context.Context, requestID string) error {
	if err := checkID(requestID); err != nil {
		return err
	}
	_, err := c.do(ctx, http.MethodPost, "/api/whitelist", map[string]string{"request_id": requestID}, 1<<20)
	return err
}

// RemoveWhitelist removes one rule (exact command and args).
func (c *Client) RemoveWhitelist(ctx context.Context, rule Rule) error {
	args := rule.Args
	if args == nil {
		args = []string{}
	}
	_, err := c.do(ctx, http.MethodPost, "/api/whitelist/remove",
		struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}{rule.Command, args}, 1<<20)
	return err
}

// Turbo reads the turbocharge state.
func (c *Client) Turbo(ctx context.Context) (TurboState, error) {
	r, err := c.do(ctx, http.MethodGet, "/api/turbocharge", nil, 1<<20)
	if err != nil {
		return TurboState{}, err
	}
	var j struct {
		Active          bool  `json:"active"`
		RemainingMs     int64 `json:"remaining_ms"`
		CooldownSeconds int64 `json:"cooldown_seconds"`
	}
	if err := json.Unmarshal(r.body, &j); err != nil {
		return TurboState{}, fmt.Errorf("decode turbo state: %w", err)
	}
	return TurboState{
		Active:    j.Active,
		Remaining: time.Duration(j.RemainingMs) * time.Millisecond,
		Cooldown:  time.Duration(j.CooldownSeconds) * time.Second,
	}, nil
}

// TurboOn switches turbocharge on for the given minutes and per-approval cooldown.
func (c *Client) TurboOn(ctx context.Context, minutes, cooldownSeconds int) error {
	_, err := c.do(ctx, http.MethodPost, "/api/turbocharge", struct {
		DurationMinutes int `json:"duration_minutes"`
		CooldownSeconds int `json:"cooldown_seconds"`
	}{minutes, cooldownSeconds}, 1<<20)
	return err
}

// TurboOff switches turbocharge off.
func (c *Client) TurboOff(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodDelete, "/api/turbocharge", nil, 1<<20)
	return err
}

// Probe checks what the token may do, without changing anything: it asks to
// approve a request id that cannot exist. nil (the relay answered 404 "not
// found") means the token is accepted for decisions. ErrForbidden means the
// token is valid but is not the approver token, and ErrUnauthorized that the
// relay does not know the token at all. A relay that has no approver token
// configured also answers 404 here, so nil covers that mode too.
func (c *Client) Probe(ctx context.Context) error {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	err := c.Approve(ctx, hex.EncodeToString(b[:]))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err == nil {
		return errors.New("probe unexpectedly succeeded")
	}
	return err
}
