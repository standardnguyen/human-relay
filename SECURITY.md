# Security Policy

## Threat model

Human Relay is designed for **private networks**. It is not suitable for public internet exposure without a TLS reverse proxy. The design assumes:

- The MCP-facing port (`:8080`) is reachable only from the sandbox running the AI agent.
- The dashboard port (`:8090`) is reachable only from trusted browsers (your LAN, VPN, or bastion host).
- The relay's SSH private key grants access to target hosts; a relay compromise grants full target access.

If either port is exposed to the public internet, or the SSH key is exfiltrated, the guarantees below do not hold.

## What's protected

- **No shell by default** — commands run via `os/exec`, not `sh -c`, so shell injection does not apply.
- **Shell mode is opt-in** — `sh -c` commands get a red warning banner in the dashboard.
- **Token auth** — the MCP port (`:8080`, both `/sse` and `/message`) requires a bearer token, as do all mutating endpoints on the dashboard port (constant-time comparison).
- **CSRF protection** — `Origin` header validation on all POST endpoints.
- **Path traversal blocked** — working directories validated against an allowlist.
- **Output capped** — stdout/stderr limited to 1MB per command.
- **Separate approver credential** — with `MHR_APPROVER_TOKEN` set, only that token may approve, deny, release output, whitelist or turbocharge; agent tokens get `403` on those calls, and the approver token is not accepted on the MCP port. `MHR_APPROVER_TOKEN_SHA256` configures it by digest so the relay host never stores the token. Unset, any authenticating token can approve (the relay warns at startup).
- **Gated output stays gated for agents** — a result approved with *Approve (Gated)* is withheld (stdout and HTTP response body, stderr, response headers) from MCP `get_result` and `list_requests` until the operator releases it, and, with `MHR_APPROVER_TOKEN` set, from the web request list for every token but the approver's. Without an approver token the web port cannot tell the operator from an agent and serves gated output to any authenticating token.
- **No script from agent text in the dashboard** — the dashboard attaches handlers with `addEventListener` and looks requests up by id, and serves a `Content-Security-Policy` with a per-response `script-src` nonce and no `'unsafe-inline'`, so agent-supplied strings cannot run with the approver token held in the browser. It refuses to be framed (`frame-ancestors 'none'`, `X-Frame-Options: DENY`), so another site cannot overlay it to clickjack an approval.
- **Approval cooldown** — server-enforced rate limit between approvals.
- **Audit log** — append-only JSONL file records every request, approval, denial, and execution result.

### No packet capture from approved commands

Approved commands run in the relay's own container and network namespace, and traffic reaching the relay's ports is plain HTTP (TLS, if you use it, ends at a proxy in front). The dashboard sends `Authorization: Bearer <approver token>` with every request it makes, so a command that could open a packet socket (`AF_PACKET`, which needs `CAP_NET_RAW`) could read dashboard or MCP traffic off the wire, including that header, and approve its own requests. Docker's default capability set includes `NET_RAW`. The shipped `docker-compose.yml` drops it from the `human-relay` service, so the socket call fails with a permission error, and `TestComposeDropsNetRaw` fails if the file stops dropping it.

If you run the relay outside this compose file, drop the capability yourself: `docker run --cap-drop NET_RAW …`, or `cap_drop: [NET_RAW]` on the service in your own compose file.

## What's not protected (yet)

- No TLS — terminate TLS at a reverse proxy.
- No per-user auth — single shared bearer token.
- Whitelist is exact-match only — no glob/regex patterns. A whitelisted request always runs with its output visible to the agent; to withhold output, leave the request unwhitelisted and use *Approve (Gated)*.
- The dashboard's `/events` metadata stream on `:8090` is unauthenticated (EventSource cannot set headers). It is read-only. This does not apply to the MCP `/sse` endpoint on `:8080`, which requires the bearer token.

### The approver token holds only up to the relay's uid

Approved and whitelisted commands run as the relay's own uid (root in the shipped container), so the separation between the approver and agents is only as strong as what a command running as that uid cannot do. The relay keeps the plaintext approver token out of `/proc/<pid>/environ` and out of every command's environment, but an approved command with enough reach can still take approval over in these ways (measured against a local relay on 2026-09-24; none is fixed in code yet):

- **The relay's heap.** Every approval request the dashboard sends carries `Authorization: Bearer <approver token>`, and those bytes stay in the relay's memory. A command that can ptrace the relay (it holds `CAP_SYS_PTRACE`, as a root process in an LXC container or on a native root install does, or `kernel.yama.ptrace_scope` is `0` and the uid matches) can read `/proc/<relay pid>/mem`, recover the token and approve from then on. This applies to `MHR_APPROVER_TOKEN_SHA256` deployments too, since the browser still sends the plaintext token. Docker's default capability set lacks `SYS_PTRACE`, and with `ptrace_scope` `1` the read is refused; a privileged container or a native root install does not have that protection.
- **Writable scripts.** `/scripts` is mounted read-write (`create_script` writes there), and a `run_script` whitelist rule matches the script's name only, not its content or arguments. A command that rewrites a whitelisted script's body gets that body auto-approved on every later `run_script` call.
- **Writable whitelist.** `whitelist.json` in the data directory is writable by the same uid. The relay reads it at startup, so a command that adds rules to it has them take effect at the next restart.

Run approved commands under a different uid than the relay, run the relay without `CAP_SYS_PTRACE`, and keep `/scripts` and the data directory out of the command uid's reach to close these. Until then, treat an approved command as able to become the approver.

## Reporting a vulnerability

If you believe you've found a security issue, please **do not open a public issue**. Instead, use GitHub's [private vulnerability reporting](https://github.com/standardnguyen/human-relay/security/advisories/new) to file a coordinated-disclosure advisory.

Please include:

- A description of the issue.
- Steps to reproduce (or a proof-of-concept).
- The commit hash you tested against.
- Any suggested fix or mitigation.

You can expect an acknowledgement within a week. Credit in the changelog for confirmed issues unless you prefer to remain anonymous.

## Supported versions

Human Relay does not maintain numbered releases; security fixes land on `main`. Deploy from `main` or a recent commit SHA.
