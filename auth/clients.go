// Package auth holds the relay's per-client bearer-token registry and the
// request-identity plumbing shared by the MCP and web servers.
//
// Before per-client tokens, a single MHR_AUTH_TOKEN authenticated every
// consumer: a leak meant a fleet-wide rotation, and no request could be
// attributed to the caller that made it. A client row now stores only the
// SHA-256 of its token, and the master token survives as the client named
// "master" so the existing fleet keeps working with no migration.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// Client is one consumer's credential row. Only the SHA-256 of the bearer
// token is ever persisted: the token itself exists in the operator's terminal
// exactly once, at mint time.
type Client struct {
	Name        string     `json:"name"`
	TokenSHA256 string     `json:"token_sha256"`
	CreatedAt   time.Time  `json:"created_at"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// Revoked reports whether the client's token has been invalidated.
func (c *Client) Revoked() bool { return c.RevokedAt != nil }

// masterClientName is the identity attributed to MHR_AUTH_TOKEN.
const masterClientName = "master"

// tokenBytes is the entropy behind a minted token: 32 bytes, base64url encoded.
const tokenBytes = 32

// lastSeenFlushInterval throttles persisting last_seen_at. touched on every
// authenticated request. Persisting on every request would turn a poll loop
// into a disk write per poll; at most one write per interval is enough for an
// operator-facing "last seen" column.
const lastSeenFlushInterval = 30 * time.Second

// Registry is the client registry, persisted as a JSON array beside the other
// relay data files (containers.json, machines.json).
type Registry struct {
	path      string
	mu        sync.RWMutex
	clients   map[string]*Client
	lastSaved time.Time
}

// NewRegistry loads the registry at path, or starts an empty one if the file
// does not exist yet.
func NewRegistry(path string) (*Registry, error) {
	r := &Registry{path: path, clients: make(map[string]*Client)}

	data, err := os.ReadFile(path)
	if err == nil {
		var list []*Client
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, c := range list {
			r.clients[c.Name] = c
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return r, nil
}

// Add mints a token for name, stores its hash, and returns the token once.
// A name already present (revoked or not) is refused, so a retired client
// cannot be silently resurrected under its old identity.
func (r *Registry) Add(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("client name is required")
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.clients[name]; ok {
		return "", fmt.Errorf("client %q already exists", name)
	}
	r.clients[name] = &Client{
		Name:        name,
		TokenSHA256: hashToken(token),
		CreatedAt:   time.Now(),
	}
	if err := r.saveLocked(); err != nil {
		delete(r.clients, name)
		return "", err
	}
	return token, nil
}

// Revoke invalidates name's token. Requests bearing it fail authentication
// from then on. Revoking an unknown name is an error; re-revoking is a no-op.
func (r *Registry) Revoke(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.clients[name]
	if !ok {
		return fmt.Errorf("client %q not found", name)
	}
	if c.RevokedAt != nil {
		return nil
	}
	now := time.Now()
	c.RevokedAt = &now
	return r.saveLocked()
}

// List returns a snapshot of the registry ordered by name.
func (r *Registry) List() []*Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sortedLocked()
}

// Verify reports whether token belongs to a known, non-revoked client and
// returns that client's name. Every row is compared (no early exit) and the
// comparison is constant-time, so a revoked token and an unknown token are
// indistinguishable by timing as well as by response.
func (r *Registry) Verify(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	want := sha256.Sum256([]byte(token))

	r.mu.Lock()
	defer r.mu.Unlock()

	var (
		name  string
		found bool
	)
	for n, c := range r.clients {
		if c.Revoked() {
			continue
		}
		if subtle.ConstantTimeCompare(want[:], digestBytes(c.TokenSHA256)) == 1 {
			name, found = n, true
		}
	}
	if !found {
		return "", false
	}
	now := time.Now()
	r.clients[name].LastSeenAt = &now
	r.maybeSaveLocked()
	return name, true
}

// Verifier authenticates a presented bearer token against the client registry
// and the master token. It is what web.AuthMiddleware consults.
type Verifier struct {
	registry     *Registry
	masterDigest [sha256.Size]byte
	hasMaster    bool
}

// NewVerifier binds a registry and the master token. An empty master token
// disables the master path.
func NewVerifier(registry *Registry, masterToken string) *Verifier {
	v := &Verifier{registry: registry}
	if masterToken != "" {
		v.masterDigest = sha256.Sum256([]byte(masterToken))
		v.hasMaster = true
	}
	return v
}

// Verify returns the client name for a valid token. The master token is
// checked first by comparing digests constant-time, then the registry.
func (v *Verifier) Verify(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	if v.hasMaster {
		digest := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(digest[:], v.masterDigest[:]) == 1 {
			return masterClientName, true
		}
	}
	if v.registry == nil {
		return "", false
	}
	return v.registry.Verify(token)
}

// hashToken is the canonical token fingerprint: hex SHA-256.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// digestBytes decodes a stored hex digest. A malformed value yields a short
// slice, which ConstantTimeCompare rejects without panicking.
func digestBytes(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

func (r *Registry) saveLocked() error {
	data, err := json.MarshalIndent(r.sortedLocked(), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(r.path, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", r.path, err)
	}
	r.lastSaved = time.Now()
	return nil
}

// maybeSaveLocked persists at most once per lastSeenFlushInterval. Caller
// holds the lock.
func (r *Registry) maybeSaveLocked() {
	if time.Since(r.lastSaved) < lastSeenFlushInterval {
		return
	}
	_ = r.saveLocked()
}

func (r *Registry) sortedLocked() []*Client {
	list := make([]*Client, 0, len(r.clients))
	for _, c := range r.clients {
		snapshot := *c
		list = append(list, &snapshot)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

type clientCtxKey struct{}

// WithClient attaches an authenticated client name to ctx.
func WithClient(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, clientCtxKey{}, name)
}

// ClientFrom returns the authenticated client name attached by WithClient, or
// "" for unauthenticated or direct (in-process) invocations.
func ClientFrom(ctx context.Context) string {
	name, _ := ctx.Value(clientCtxKey{}).(string)
	return name
}
