# Human Relay

**Human-in-the-loop command execution for AI agents.**

Human Relay is an [MCP](https://modelcontextprotocol.io/) server that sits between your AI agent and your infrastructure. Agents request commands; you approve or deny them in a web dashboard; only then do they execute.

```
┌─────────────┐   JSON-RPC/SSE   ┌──────────────┐     HTTP      ┌─────────┐
│  AI Agent    │◄────────────────►│ Human Relay  │◄────────────►│ Browser │
│ (Claude Code,│   :8080/sse     │              │   :8090      │  (You)  │
│  Cursor, etc)│                 │  ┌─────────┐ │              └─────────┘
└─────────────┘                 │  │ Request │ │    Approve / Deny
                                │  │  Queue  │ │         │
                                │  └─────────┘ │         ▼
                                │  ┌─────────┐ │   ┌──────────┐
                                │  │Executor │─┼──►│ ssh, bash,│
                                │  └─────────┘ │   │ any cmd   │
                                └──────────────┘   └──────────┘
```

## Why

AI coding agents are powerful but run in sandboxes. When they need to touch production systems — restart a service, check disk usage, deploy a config — you either give them SSH keys (dangerous) or copy-paste commands yourself (tedious).

Human Relay gives agents a way to *ask* for command execution. You stay in the loop, review each command, and approve with a click. The agent gets the output and continues working.

## Quickstart

Most users want to run the relay on the same machine their agent runs on. No Docker required — it's a single binary.

### 1. Build and run

```bash
# Requires Go 1.24+
git clone https://github.com/standardnguyen/human-relay.git
cd human-relay
go build -o human-relay .

# Generate a dashboard token and start the server
export MHR_AUTH_TOKEN=$(openssl rand -hex 16)
echo "Dashboard token: $MHR_AUTH_TOKEN"
./human-relay
```

The MCP server starts on `:8080` and the web dashboard on `:8090`. Open `http://localhost:8090` in your browser — that's where you'll approve commands.

### 2. Connect your agent

Add to your MCP client config (e.g. Claude Code `~/.claude/settings.json`):

```json
{
  "mcpServers": {
    "human-relay": {
      "command": "npx",
      "args": ["mcp-remote", "http://localhost:8080/sse", "--allow-http"]
    }
  }
}
```

The agent now has access to the `request_command`, `get_result`, and other tools. When the agent submits a command, it appears in the dashboard for your approval.

### Docker (alternative)

If you prefer running the relay in a container (e.g., on a remote server):

```bash
cp .env.example .env
# Edit .env: set MHR_AUTH_TOKEN, MHR_HOST_IP (see Configuration below)

# The container needs an SSH key to execute commands on the host.
# Generate one if you don't have one:
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519 -N "" -q
cat ~/.ssh/id_ed25519.pub >> ~/.ssh/authorized_keys

docker compose up -d --build
```

**SSH key requirement:** The `docker-compose.yml` mounts `~/.ssh/id_ed25519` into the container as a read-only volume. The relay uses this key to SSH back to the host (at `MHR_HOST_IP`) to execute approved commands. The key must exist before starting the container, and its public key must be in `authorized_keys` on the target host.

## MCP Tools

| Tool | Description | Requires Approval |
|------|-------------|:-:|
| `request_command` | Submit a command for human approval | Yes |
| `get_result` | Poll for command result (supports blocking with timeout) | No |
| `list_requests` | List requests, optionally filtered by status | No |
| `register_container` | Register a remote host in the container registry | No |
| `list_containers` | List registered containers | No |
| `exec_container` | Execute a command on a registered remote host via SSH | Yes |
| `write_file` | Deploy a file to a remote host (base64-encoded, binary-safe) | Yes |

### Basic flow

```
Agent: request_command(command="df", args=["-h"], reason="Check disk space")
  → { "request_id": "a1b2c3d4", "status": "pending" }

  [You review and approve in the dashboard]

Agent: get_result(request_id="a1b2c3d4", timeout=30)
  → { "status": "complete", "result": { "exit_code": 0, "stdout": "..." } }
```

**Note:** The MCP transport is JSON-RPC over SSE. The agent must hold an open SSE connection to `/sse` — this is the session. Tool calls are POSTed to the `/message` endpoint returned by the SSE stream, and responses come back over that same SSE connection, not as HTTP response bodies. MCP client libraries (like `mcp-remote`) handle this automatically.

### Container routing

For managing remote hosts, register them once and `exec_container` handles SSH routing:

```
Agent: register_container(ctid=133, ip="10.0.0.50", hostname="webserver", has_relay_ssh=true)
Agent: exec_container(ctid=133, command="docker", args=["compose","ps"], reason="Check services")
  → routes to: ssh root@10.0.0.50 -- docker compose ps
```

## Dashboard

The web UI shows pending requests with full command details, approve/deny buttons, and a history of completed requests. Real-time updates via SSE — no polling.

Features:
- Shell mode commands highlighted with a red warning banner
- Non-ASCII characters flagged to catch homoglyph attacks
- 30-second cooldown between approvals (prevents reflexive clicking)
- Turbocharge mode to temporarily reduce cooldown during batch operations
- Browser notifications for new requests

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `MHR_AUTH_TOKEN` | (required) | Bearer token for dashboard API authentication |
| `MHR_MCP_PORT` | `8080` | MCP SSE server port |
| `MHR_WEB_PORT` | `8090` | Web dashboard port |
| `MHR_DEFAULT_TIMEOUT` | `30` | Default command timeout (seconds) |
| `MHR_MAX_TIMEOUT` | `300` | Maximum allowed timeout |
| `MHR_APPROVAL_COOLDOWN` | `30` | Seconds between approvals (0 to disable) |
| `MHR_ALLOWED_DIRS` | (none) | Comma-separated allowed working directories |
| `MHR_DATA_DIR` | `/opt/human-relay/data` | Persistent data directory (audit log, container registry) |
| `MHR_HOST_IP` | (none) | Fallback host IP for `exec_container` routing when direct SSH is unavailable |

## Security

Human Relay is designed for **private networks**. It is not suitable for public internet exposure without a TLS reverse proxy.

### What's protected

- **No shell by default** — commands run via `os/exec`, not `sh -c`, so shell injection doesn't apply
- **Shell mode is opt-in** — `sh -c` commands get a red warning banner in the dashboard
- **Token auth** — all mutations require a bearer token (constant-time comparison)
- **CSRF protection** — Origin header validation on all POST endpoints
- **Path traversal blocked** — working directories validated against an allowlist
- **Output capped** — stdout/stderr limited to 1MB per command
- **Approval cooldown** — server-enforced rate limit between approvals
- **Audit log** — append-only JSONL file records every request, approval, denial, and execution result

### What's not (yet)

- No TLS (use a reverse proxy)
- No per-user auth (single shared token)
- No command allowlist (the human is the filter)
- SSE metadata endpoint is unauthenticated (EventSource can't set headers)

## Development

```bash
# Build
go build -o human-relay .

# Run
MHR_AUTH_TOKEN=test-token ./human-relay

# Run tests
HUMAN_RELAY_BIN=$(pwd)/human-relay go test -v -count=1 ./...
```

### Project structure

```
human-relay/
├── main.go              # Entry point, config, server startup
├── audit/               # Append-only JSONL audit logger
├── mcp/                 # MCP protocol: JSON-RPC over SSE, tool handlers
├── store/               # In-memory request queue
├── containers/          # JSON-backed container/host registry
├── executor/            # Command execution (timeout, output capping)
├── web/                 # Dashboard HTTP handlers, SSE, auth
│   └── templates/       # Single-page dashboard (vanilla JS)
├── integration/         # Integration test suite
├── Dockerfile           # Multi-stage build (Go 1.24 + Debian slim)
└── docker-compose.yml   # Ready-to-run with Docker Compose
```

Single Go binary. Zero external dependencies at runtime. ~2,500 lines of Go.