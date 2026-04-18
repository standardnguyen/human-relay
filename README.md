# Human Relay

[![Go Report Card](https://goreportcard.com/badge/github.com/standardnguyen/human-relay)](https://goreportcard.com/report/github.com/standardnguyen/human-relay)
[![License: Petty SL 2.1.2](https://img.shields.io/badge/license-Petty%20SL%202.1.2-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/standardnguyen/human-relay)](go.mod)

**Human-in-the-loop command execution for AI agents.**

Human Relay is an [MCP](https://modelcontextprotocol.io/) server that acts as a human approval gate between AI coding agents and the hosts they operate on. Agents submit commands over MCP; you approve or deny each from a web dashboard; the relay executes approved commands over SSH and returns the output. Every request and decision lands in an append-only audit log.

```
  Sandboxed                    Isolated                     Your infra
┌─────────────┐  JSON-RPC/SSE  ┌──────────────┐    SSH     ┌──────────┐
│  AI Agent    │◄──────────────►│ Human Relay  │───────────►│ Target   │
│ (Claude Code,│  :8080/sse    │  (Docker)    │            │ hosts    │
│  Cursor, etc)│               │              │            └──────────┘
└─────────────┘               │  ┌─────────┐ │   HTTP    ┌─────────┐
  Can only reach               │  │ Request │ │◄─────────►│ Browser │
  the MCP port                 │  │  Queue  │ │  :8090    │  (You)  │
                               │  └─────────┘ │           └─────────┘
                               └──────────────┘  Approve / Deny
```

## Why

AI coding agents are powerful but run in sandboxes. When they need to touch production systems — restart a service, check disk usage, deploy a config — you either give them SSH keys (dangerous) or copy-paste commands yourself (tedious).

Human Relay gives agents a way to *ask* for command execution. You stay in the loop, review each command, and approve with a click. The agent gets the output and continues working.

## Isolation model

Human Relay is only useful if the agent **cannot bypass it**. This requires three components in separate trust boundaries:

| Component | Where it runs | Can reach |
|-----------|--------------|-----------|
| AI Agent | Sandboxed container or VM | Relay MCP port (`:8080`) only |
| Human Relay | Its own container (Docker) | SSH to target hosts, dashboard UI |
| Target hosts | Your infrastructure | N/A — commands are pushed to them |

If the agent runs on the same machine as the relay with no containerization, it can execute commands directly and the relay is just theater. If the relay runs directly on a target host with no container, a relay compromise gives full host access.

**Recommended setup:** Run the relay in a Docker container on a machine the agent cannot directly SSH into. The agent connects to the relay over the network. The relay SSHes to your infrastructure to execute approved commands.

**The human is the filter.** Human Relay is not a replacement for understanding what you're approving. An agent could request `cat /root/.ssh/id_ed25519` and exfiltrate the relay's private key in the output. It could chain innocent-looking commands that together do something destructive. The dashboard gives you visibility, cooldowns give you time to think, and the audit log gives you a paper trail — but ultimately it comes down to the operator knowing what each command does before clicking approve.

## Quickstart

### 1. Deploy the relay

On the machine that will host the relay (a server, VM, or container with Docker):

```bash
git clone https://github.com/standardnguyen/human-relay.git
cd human-relay
cp .env.example .env
# Edit .env: set MHR_AUTH_TOKEN to a random secret
#            set MHR_HOST_IP to the IP of the machine you want commands to run on

# The relay container needs an SSH key to reach target hosts.
# Generate one if you don't have one:
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519 -N "" -q

# Authorize the key on each target host:
cat ~/.ssh/id_ed25519.pub >> ~/.ssh/authorized_keys  # for the local host
# ssh-copy-id -i ~/.ssh/id_ed25519.pub root@other-host  # for remote targets

docker compose up -d --build
```

The `docker-compose.yml` mounts `~/.ssh/id_ed25519` into the container as a read-only volume. The relay uses this key to SSH to target hosts and execute approved commands.

### 2. Connect your agent

From wherever your agent runs (a different machine, container, or VM), add to your MCP client config (e.g. Claude Code `~/.claude/settings.json`):

```json
{
  "mcpServers": {
    "human-relay": {
      "command": "npx",
      "args": ["mcp-remote", "http://RELAY_HOST:8080/sse", "--allow-http"]
    }
  }
}
```

Replace `RELAY_HOST` with the IP or hostname of the machine running the relay.

Human Relay works with any MCP client that supports remote SSE transport via `mcp-remote` — Claude Code, Cursor, Windsurf, Continue, Cline, Zed, Goose. Primary development and testing is against Claude Code; client-specific quirks are tracked in GitHub issues.

### 3. Approve commands

Open `http://RELAY_HOST:8090` in your browser. When the agent submits a command, it appears here for your approval.

### Running without Docker (development only)

For local development and testing, you can run the binary directly:

```bash
# Requires Go 1.24+
go build -o human-relay .
MHR_AUTH_TOKEN=dev-token ./human-relay
```

This runs the relay and executes approved commands directly on your machine with no isolation. Fine for development, not for production.

## MCP Tools

| Tool | Description | Requires Approval |
|------|-------------|:-:|
| `request_command` | Submit a command for human approval | Yes |
| `get_result` | Poll for command result (supports blocking with timeout) | No |
| `list_requests` | List requests, optionally filtered by status | No |
| `register_container` | Register a remote host in the container registry | No |
| `list_containers` | List registered containers | No |
| `exec_container` | Execute a command on a registered remote host via SSH | Yes |
| `write_file` | Deploy a file to a remote host; accepts plaintext `content` or base64 `content_base64` (binary-safe) | Yes |
| `withdraw_request` | Retract a pending request the agent no longer wants executed (with a reason; shown as WITHDRAWN in the dashboard) | No |

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
- Command whitelist with auto-approval for trusted command+args patterns
- Collapsible whitelist panel; one-click "Whitelist" button on completed requests

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
| `MHR_DATA_DIR` | `/opt/human-relay/data` | Persistent data directory (audit log, container registry, whitelist, SSH known hosts) |
| `MHR_HOST_IP` | (none) | Fallback host IP for `exec_container` routing when direct SSH is unavailable |
| `MHR_WHITELIST_FILE` | `<data_dir>/whitelist.json` | Path to whitelist rules file; matching commands are auto-approved |
| `MHR_SSH_CONFIG` | (none) | Path to custom SSH config; prepends `-F <path>` to all SSH commands |

## Security

Human Relay is designed for **private networks** — not public internet exposure without a TLS reverse proxy. See [`SECURITY.md`](SECURITY.md) for the full threat model, what the design does and does not protect against, and how to report vulnerabilities.

## Alternatives

Other ways people keep AI agents from doing destructive things:

- **Built-in approval modes in MCP clients** — Claude Code's permission prompts, Cursor's auto-run gating. Gate IDE actions well but don't cover arbitrary shell commands across remote hosts, and approvals don't persist outside the session.
- **Filter/proxy MCP servers** that wrap a single upstream MCP server and add a confirm step. Narrower scope (one server at a time), no dashboard, no cross-host audit log.
- **SSH bastions with command logging** catch things after the fact; no interactive approval step, no agent-side context.
- **Copy-paste workflow** — the agent prints a shell command and you paste it into a terminal yourself. No MCP needed, no audit trail, tedious at scale.

Human Relay's niche: a single approval surface for *every* shell command an agent wants to run, across any number of SSH-reachable hosts, with an append-only audit log, per-request reasoning from the agent, and a reusable command whitelist for routine operations.

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
├── whitelist/           # JSON-backed command whitelist (exact match)
├── web/                 # Dashboard HTTP handlers, SSE, auth
│   └── templates/       # Single-page dashboard (vanilla JS)
├── integration/         # Integration test suite
├── Dockerfile           # Multi-stage build (Go 1.24 + Debian slim)
└── docker-compose.yml   # Ready-to-run with Docker Compose
```

Single Go binary. Zero external dependencies at runtime. ~2,500 lines of Go.