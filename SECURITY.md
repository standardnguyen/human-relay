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
- **Token auth** — all mutating endpoints require a bearer token (constant-time comparison).
- **CSRF protection** — `Origin` header validation on all POST endpoints.
- **Path traversal blocked** — working directories validated against an allowlist.
- **Output capped** — stdout/stderr limited to 1MB per command.
- **Approval cooldown** — server-enforced rate limit between approvals.
- **Audit log** — append-only JSONL file records every request, approval, denial, and execution result.

## What's not protected (yet)

- No TLS — terminate TLS at a reverse proxy.
- No per-user auth — single shared bearer token.
- Whitelist is exact-match only — no glob/regex patterns.
- The SSE metadata endpoint is unauthenticated (EventSource cannot set headers).

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
