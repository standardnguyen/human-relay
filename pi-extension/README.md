# pi-extension/relay-gate

A pi-coding-agent extension that forwards every `tool_call` event to the
human-relay's `/api/permission/check` endpoint for an allow / deny / ask
verdict. Together with the relay-side `permissions/` package, this gives pi
(and other agents lacking native gating) a centralized permission system
backed by file-based rules that survive restarts.

CC has its own harness-native gating (`settings.local.json`) and does **not**
use this. Going through the relay would just double-prompt.

## Install

This file ships in the human-relay repo so the pi-side and relay-side stay
version-locked. To activate on a pi host:

```bash
# Clone or pull the repo on the pi host
git clone ssh://git@192.168.10.57:2222/administrator/human-relay.git /opt/human-relay
# or: git -C /opt/human-relay pull origin main

# Symlink the extension into pi's extensions dir
ln -sf /opt/human-relay/pi-extension/relay-gate.ts ~/.pi/agent/extensions/relay-gate.ts
```

The symlink is preferred over a copy — pulling main updates the extension in
place; no second step.

## Configuration

Set these environment variables before launching pi (e.g. via your shell rc
file or a launcher script):

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `PI_RELAY_GATE_URL` | **yes** | — | Relay web URL, e.g. `http://192.168.10.88:8090`. Note: **web port** (8090), not the MCP port (8080). |
| `PI_RELAY_GATE_TOKEN` | **yes** | — | Bearer token. Same value as the relay's `MHR_AUTH_TOKEN`. |
| `PI_RELAY_GATE_CLIENT` | no | `pi` | Cosmetic tag surfaced in the relay dashboard queue + audit log. Useful when multiple pi instances share one relay. |
| `PI_RELAY_GATE_TIMEOUT_MS` | no | `5000` | Per-HTTP-request timeout. |
| `PI_RELAY_GATE_ASK_TIMEOUT_S` | no | `240` | Max seconds to wait on an `ask` verdict before failing closed. Relay queue timeout is 300s, so keep this below that. |

If `PI_RELAY_GATE_URL` or `PI_RELAY_GATE_TOKEN` are unset, the extension logs
an error and disables itself — pi runs as if the extension weren't installed.
This is the explicit opt-out path.

## Failure mode

The extension is **fail-closed**: any error reaching the relay (network
failure, 5xx response, ask timeout) blocks the tool call. This is the
conservative default per the FC-014 design decision. There is no
fail-open knob; if pi needs to run somewhere the relay can't be reached,
unset `PI_RELAY_GATE_URL` and pi will run ungated.

## Rules

Rules live on the relay at `<MHR_DATA_DIR>/permissions.json` (default
`/opt/human-relay/data/permissions.json`). Schema:

```json
{
  "allow": ["Bash(ls:*)", "Read(/root/personal-wiki/**)"],
  "deny":  ["Bash(rm -rf /:*)"],
  "ask":   ["Bash(git push:*)"]
}
```

See the relay's `permissions/permissions.go` for the matcher semantics:
Bash uses command-prefix matching (`:*` is the wildcard suffix); file tools
use glob patterns (`**` is multi-segment, `*` is single-segment).

The rule file is read once at relay startup. After editing it, restart the
relay container: `ssh root@192.168.10.88 'cd /opt/human-relay && docker compose restart human-relay'`.

## Audit

Every permission check is logged to the relay's audit log
(`<MHR_DATA_DIR>/audit.log`) as a `permission_check` event with the tool
name, input, reason, client tag, verdict, and rule_id. `ask`-routed checks
that get approved or denied add `permission_approved` or `request_denied`
events.

## Reference architectures

This extension intentionally stays minimal — it does one thing: forward the
permission decision to the relay. For more sophisticated in-pi-process
permission management (interactive prompts, per-skill rules, etc.) see:

- `MasuRii/pi-permission-system` — full in-process permission manager
- `gotgenes/pi-packages` — monorepo with `permission-forwarding.ts`

These are alternative architectures, not complements. If you install one of
those, don't also install relay-gate; they'd conflict on the `tool_call`
hook.
