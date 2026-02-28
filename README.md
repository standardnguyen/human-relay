# Human Relay

An MCP server that mediates between AI agents and command execution. Agents submit commands via the MCP protocol (JSON-RPC over SSE); a human operator reviews each request in a web dashboard and approves or denies it.

## How It Works

1. An AI agent connects to the MCP SSE endpoint and calls `request_command` with a command, arguments, and reason.
2. The request appears in the web dashboard as a pending card.
3. The human operator reviews the command and clicks Approve or Deny.
4. If approved, the server executes the command and returns stdout/stderr/exit code to the agent via `get_result`.
5. If denied, the agent receives the denial reason.

## MCP Tools

| Tool | Description |
|------|-------------|
| `request_command` | Submit a command for human approval. Returns a request ID. |
| `get_result` | Poll for the result of a submitted command. Supports blocking poll with timeout. |
| `list_requests` | List all requests, optionally filtered by status. |

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `MHR_AUTH_TOKEN` | (required) | Bearer token for web dashboard API authentication |
| `MHR_MCP_PORT` | 8080 | Port for the MCP SSE server |
| `MHR_WEB_PORT` | 8090 | Port for the web dashboard |
| `MHR_DEFAULT_TIMEOUT` | 30 | Default command timeout in seconds |
| `MHR_MAX_TIMEOUT` | 300 | Maximum allowed command timeout in seconds |
| `MHR_ALLOWED_DIRS` | (none) | Comma-separated list of allowed working directories |

## Security Model

Human Relay is designed for **private network / Tailnet deployment**. It is not suitable for public internet exposure.

### Implemented Mitigations

- **Bearer token authentication** on all mutation and data API endpoints
- **CSRF validation** via Origin header checking on POST requests
- **Path traversal protection** — working directories are canonicalized with `filepath.Clean` and validated against an allowlist
- **Output truncation** — stdout and stderr are capped at 1MB to prevent memory exhaustion
- **Bidi character stripping** — bidirectional Unicode control characters are replaced with `[BIDI]` in the frontend to prevent reorder attacks
- **Non-ASCII warning** — commands containing non-ASCII characters are flagged with a visual warning to catch homoglyph substitution
- **Shell mode warning** — shell commands (`sh -c`) get a prominent red warning banner and card highlight
- **Double-approval protection** — approving an already-approved request returns 409 Conflict
- **Approval cooldown** — 30-second client-side cooldown between approvals to prevent reflexive clicking

### Known Unmitigated Threats

These are accepted risks for the current deployment context (private Tailnet):

- **No TLS.** Traffic between agent, server, and dashboard is unencrypted. Acceptable on a private network; would need a reverse proxy (Caddy/nginx) for any other deployment.
- **No per-user authentication.** Anyone with the bearer token has full access. There is no user identity, session management, or role separation.
- **SSE metadata leak.** The `/events` SSE endpoint is unauthenticated (EventSource cannot set headers). An attacker on the network could observe command names, reasons, and statuses in real time. Read-only; no mutations possible.
- **No command allowlist.** Any command can be submitted. The human operator is the only filter.
- **No persistent audit log.** Approved commands and their outputs are stored in memory only. Server restart loses all history.
- **Homoglyphs are flagged, not blocked.** The non-ASCII warning helps a human spot suspicious characters, but does not prevent submission.
- **Approval cooldown is client-side only.** Reloading the page bypasses the 30-second cooldown. It is a UX nudge against fatigue, not a hard rate limit.

### Future Versions

- **Persistent audit log** — append-only log file recording every approval, denial, command, and output. Highest-priority next step.
- **Per-user authentication** — individual operator accounts with role separation.
- **Command allowlist/denylist** — regex-based filtering of submitted commands.

## Development

```bash
# Build
go build -o human-relay .

# Run
MHR_AUTH_TOKEN=your-token ./human-relay

# Run integration tests (from the human-relay-tests repo)
HUMAN_RELAY_BIN=/path/to/human-relay go test -v -count=1 ./...
```

## Architecture

```
Agent (Claude Code, etc.)
  |
  |  JSON-RPC over SSE (MCP protocol)
  |
  +---> MCP Server (:8080/sse, /message)
  |       +-- store (in-memory request queue)
  |
  +---> Web Dashboard (:8090)
          +-- GET /           -> dashboard HTML (no auth)
          +-- GET /events     -> SSE stream (no auth)
          +-- GET /api/requests     -> list requests (auth required)
          +-- POST /api/requests/{id}/approve|deny (auth required)
                +-- executor -> sh -c or direct exec -> stdout/stderr
```
