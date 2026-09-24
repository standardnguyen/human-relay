# Changelog

Notable changes to Human Relay. Format loosely follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Version tags are not maintained yet; sections below group by merge date. See `git log` for the full commit-level history.

## Unreleased

### Security
- **Stored DOM-XSS in the dashboard and `/chat` fixed.** An agent could end the `onclick` attribute the dashboard built from a request's `command` (or, through a literal `&quot;`, the JS string built from its `args`), and `/chat` wrote a signal conversation key into an `onclick` unescaped. The injected handler ran with the token in the viewer's localStorage, so with `MHR_APPROVER_TOKEN` set it could approve and whitelist the agent's own requests, or read the approver token. Both pages now attach every control through one delegated `addEventListener` that looks the request up by id; no agent text is written into an attribute or a JS string. Both pages are served with a `Content-Security-Policy` whose `script-src` is a fresh per-response nonce with no `'unsafe-inline'`, so an inline handler would not run even if a render path failed to escape.
- **A plaintext `MHR_APPROVER_TOKEN` no longer stays in `/proc/<pid>/environ`.** `os.Unsetenv` only edits Go's copy; the kernel's copy of the initial environment was readable by every command the relay ran. The relay now re-executes itself at startup (same PID) with the token replaced by its SHA-256.
- **An empty `MHR_APPROVER_TOKEN_SHA256=` next to `MHR_APPROVER_TOKEN` no longer switches the approver gate off.** The startup re-exec that swaps the plaintext token for its digest kept an empty digest entry ahead of the one it appended; Go reads the first entry, so the relay came up in legacy mode, where every agent token approves, with only a startup warning. The re-exec now drops every entry of both variables before appending the digest, the relay refuses to start when either variable appears more than once in its environment, and it refuses to start rather than fall back to legacy mode when an approver variable carries a value the gate did not take up. Both variables absent or empty is still legacy mode with the startup warning.
- **Gated output is no longer readable by agent tokens over the web API.** `GET /api/requests` returned the full `result` of an output-gated, unreleased request to any token, so an agent holding a token that also authenticates on the web port read what MCP `get_result` withheld, and the approver-only Release gated nothing. With `MHR_APPROVER_TOKEN` set, the request list now withholds stdout (an HTTP response body lands there), stderr and response headers of gated, unreleased requests from every token but the approver's; exit and HTTP status codes and `output_gated` stay. MCP `get_result` and `list_requests` now withhold the response headers of a gated `http_request` too, which they had returned all along. Legacy mode (no approver) cannot tell the operator's browser from an agent on the web port and still serves gated output there to every token; MCP withholds it in both modes. The request's own stdin (`/api/requests/{id}/content`), `/events` frames and `/api/permission/check/{id}` never carried output and are unchanged.

### Added
- **`MHR_APPROVER_TOKEN` — an approver credential separate from agent tokens.** When set, approve, approve-gated, deny, release, whitelist add/remove and turbocharge on/off accept only the approver token and answer any other token with `403` ("this token cannot approve"). Reads (the request list, request content, whitelist and turbo state, `/events`) and agent writes such as `/api/permission/check` accept every token as before. The approver token is verified on the web port only, never on the MCP port, and the relay refuses to start if it equals `MHR_AUTH_TOKEN` or a client token. `MHR_APPROVER_TOKEN_SHA256` configures the same thing from the token's SHA-256, so the relay never holds the token; either variable is dropped from the relay's environment at startup so approved commands do not inherit it. Decision events in the audit log gain `approved_by` (`approver` or `legacy-shared-token`) and `approver_client`. Unset keeps the previous behaviour and logs a startup warning. The dashboard explains a `403` and offers to swap the stored token.
- `delete_container` MCP tool — removes a registry entry (does not touch the actual container). For retiring stale/recycled CTID registrations, e.g. a deprecated pseudo-CTID migrated to the machine registry. Approval-gated (see Security below), idempotent-hint.
- `workflow_dispatch` trigger on the CI workflow — allows manual redeploys from the Actions UI without a dummy commit (added after a merge-push failed to dispatch during a Forgejo outage).
- **Machine registry — first-class non-LXC SSH targets.** A new string-keyed `machines` registry (`<data_dir>/machines.json`) is the proper home for SSH targets that aren't Proxmox containers (Windows workstations, bare metal, VMs, WSL), replacing the "pseudo-CTID" hack of registering fake CTIDs in the container registry. New MCP tools: `register_machine` (name, host, ssh_user, shell, optional identity_file), `list_machines`, `delete_machine`, `exec_machine`. Each machine has a `shell` field — `posix` (default) or `powershell` — that drives remote command construction.
- **Windows / PowerShell as a real target.** For `powershell` machines, `exec_machine` (shell mode), `write_file`, and `install_ssh_key` build the remote command via `powershell -NoProfile -EncodedCommand <base64-UTF16LE>` (decoded for the reviewer by the existing approval-pane decoder), so paths and scripts never fight ssh↔shell quoting. `write_file` to a powershell machine base64-streams the content over stdin and decodes it on the far side with `[IO.File]::WriteAllBytes` (binary-safe); `install_ssh_key` appends to `%USERPROFILE%\.ssh\authorized_keys`. `write_file`/`install_ssh_key` gained a `machine` routing param; machine write targets accept Windows-style paths (`C:\...`).
- `create_then_run` MCP tool combines `create_script` + `run_script` into a single approval. Default target is `/opt/human-relay/scripts/oneshot/<name>.{sh,py,json}` (extension auto-detected from content, same rules as `create_script`). A slash in `<name>` is respected as-is for deliberate non-oneshot subdirs. Refuses on collision (cross-extension, so `create_then_run("foo", <shell>)` won't shadow an existing `foo.py`). Script persists on disk after the run — re-runnable via `run_script(name="oneshot/<name>")`.
- `run_script` accepts subpath names (e.g. `oneshot/foo`). New `validScriptPathRe` regex allows alphanumeric segments joined by single slashes; rejects traversal, leading/trailing/double slashes, and `.`/`..` segments.
- `write_file` pre-approval overwrite warning. Before queuing the approval, the relay probes the target via SSH with `stat -c '%s %Y' <path>` (same routing as the write — direct SSH or pct exec). If the file exists, the approval reason gains an `[OVERWRITE: <size>B, modified <YYYY-MM-DD HH:MM>]` line so the reviewer sees it before deciding. Fail-open: any probe error (SSH failure, permission denied, timeout) proceeds with the write and logs a `write_file_probe_failed` audit event. 3-second probe timeout.
- `.github/` scaffolding — issue templates (bug / feature), PR template, CODEOWNERS.
- `CHANGELOG.md` (this file).

### Changed
- Module path is now `github.com/standardnguyen/human-relay` (was an internal path). Unblocks `go install`, Go Report Card, and pkg.go.dev.
- README rewritten for discoverability: approval-gate framing, explicit MCP client list (Claude Code, Cursor, Windsurf, Continue, Cline, Zed, Goose), Alternatives section, badge row.
- Security section moved to `SECURITY.md`; README now links to it.
- `run_script` validator no longer rejects slashes outright — they now denote subpaths under the scripts directory.

### Security
- **The four registry-mutating tools now go through human approval.** `register_container`, `delete_container`, `register_machine` and `delete_machine` used to write `containers.json` / `machines.json` synchronously from the MCP port and return the result — the only mutating tools with no human in the loop. They now queue a `registry_op` request like everything else: the tool validates its arguments, returns `{"request_id": ..., "status": "pending"}`, and the registry is written only after you approve in the dashboard. This matters because the container registry is what decides where `exec_container` / `write_file` SSH *lands* — silently re-pointing a CTID at a different IP was a one-call, zero-click operation. **Breaking for agents:** these tools no longer return the `Container`/`Machine` object; poll `get_result` (the approved request's stdout carries the same JSON) or call `list_containers` / `list_machines`. Argument validation (including the option-injectable `ssh_user` check) still rejects at submission, so a malformed call never reaches the queue.
- **The MCP port (`:8080`) now requires the bearer token.** Both `/sse` and `/message` go through the same constant-time `Authorization: Bearer $MHR_AUTH_TOKEN` check the dashboard API uses. Previously the port that exposes the entire tool surface (`request_command`, `write_file`, `exec_container`, ...) was served with no authentication at all, so anything that could reach it could queue commands as the agent. No CSRF middleware there — nothing on that port is browser-originated. **Breaking for clients:** MCP client configs must now send the header (see README → Connect your agent); an unauthenticated client gets `401`.

## 2026-04-17

### Added
- `withdraw_request` MCP tool lets an agent retract a pending request it no longer wants executed. Withdrawn requests stay visible in the dashboard with a reason and a Mark Read button.
- `write_file` accepts plaintext `content` alongside the existing `content_base64`. Use plaintext for text files; base64 is only needed for binaries or byte sequences with embedded nulls.

## 2026-04-16

### Added
- `run_script` accepts a positional `args` array for shell and Python scripts.

## 2026-04-07

### Added
- Shell (`.sh`) script support in `create_script` / `run_script`.

### Changed
- License updated to Petty Software License v2.1.2.

## 2026-04-01

### Added
- `create_script` / `run_script` tools with JSON pipeline engine and Python script support.

### Fixed
- Whitelist button works for script-type requests.

## 2026-03-13

### Changed
- Hard rejection of `bash -c` argv-splitting patterns that caused crontab wipe incidents.
