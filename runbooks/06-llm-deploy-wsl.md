# Runbook 06: LLM-Driven Deploy — WSL

**Type:** Claude-control (LLM-driven)
**Trigger:** Pre-release, Windows compatibility validation
**Duration:** ~15-20 minutes

## Purpose

Validate that an LLM can deploy human-relay inside Windows Subsystem for Linux. WSL is weird — networking is different (localhost vs. WSL IP vs. Windows host IP), Docker Desktop vs. dockerd-in-WSL is a fork, and file permissions behave differently. This test catches WSL-specific gotchas in the docs.

## Prerequisites

- Windows machine (physical or VM) with:
  - WSL2 installed with a Debian/Ubuntu distro
  - Docker Desktop with WSL2 backend, OR Docker installed natively inside WSL
- `claude` CLI accessible from within WSL
- Repo cloned inside WSL at `/opt/human-relay`

## Setup

### 1. Provision Windows VM (if using CI)

For CI, use a Windows Server VM with WSL2 enabled:

```powershell
# On the Windows host
wsl --install -d Debian
wsl -d Debian -u root -- bash -c "apt-get update && apt-get install -y git docker.io docker-compose-plugin"
wsl -d Debian -u root -- bash -c "git clone <repo-url> /opt/human-relay"
```

### 2. Start Docker Inside WSL

```bash
wsl -d Debian -u root -- bash -c "dockerd &"
# OR if using Docker Desktop, ensure WSL2 backend is enabled
```

## Test Execution

### 3. Run the LLM Agent Inside WSL

```bash
wsl -d Debian -u root -- claude --dangerously-skip-permissions \
  --print \
  --model claude-opus-4-6 \
  --prompt "$(cat <<'PROMPT'
You are inside a WSL2 Debian instance on a Windows machine.
Docker is available (either Docker Desktop's WSL2 backend or native dockerd).
The human-relay repo is at /opt/human-relay.

Your task: deploy human-relay using Docker Compose.

IMPORTANT WSL-SPECIFIC CHALLENGES:
- Networking: WSL has its own IP. localhost from WSL may or may not reach Windows host.
  Figure out what IP to use for MHR_HOST_IP.
- Docker: might be Docker Desktop (socket at /var/run/docker.sock via WSL integration)
  or native dockerd. Figure out which one you have.
- SSH: the container needs to SSH back to execute commands. WSL's SSH setup may differ.

Requirements:
1. Configure .env appropriately for the WSL environment
2. Start the stack
3. Verify dashboard and MCP endpoint are accessible
4. Submit and approve a test command
5. Note any WSL-specific issues you encountered

Print DEPLOY_SUCCESS or DEPLOY_FAILED, followed by a list of WSL-specific issues.
PROMPT
)" 2>&1 | tee /tmp/llm-deploy-wsl.log
```

### 4. Evaluate

```bash
grep -q "DEPLOY_SUCCESS" /tmp/llm-deploy-wsl.log && echo "PASS" || echo "FAIL"

# Also capture WSL-specific issues for doc improvement
grep -A 50 "WSL-specific issues" /tmp/llm-deploy-wsl.log > /tmp/wsl-issues.txt
```

## WSL-Specific Failure Points

These are the things we expect to break and want to document fixes for:

| Issue | Detail |
|-------|--------|
| **MHR_HOST_IP confusion** | In WSL, `hostname -I` gives the WSL vEthernet IP, not the Windows host IP. The container needs to SSH to... where? If sshd runs in WSL, use the WSL IP. If sshd runs on Windows, use the Windows IP. |
| **Docker socket** | Docker Desktop exposes the socket into WSL automatically. Native dockerd needs `service docker start` or `dockerd &`. The LLM needs to figure out which situation it's in. |
| **Port forwarding** | WSL2 uses a NAT. Ports exposed in WSL may not be reachable from Windows without `netsh` forwarding. The dashboard might only be reachable from within WSL. |
| **File permissions** | WSL's `/root/.ssh/` may have wrong permissions if the repo was cloned from a Windows mount (`/mnt/c/...`). |
| **systemd** | Older WSL2 distros don't have systemd. `systemctl` won't work. Docker needs manual start. |

## What Failures Tell Us

Every WSL failure means the README needs a WSL-specific section or callout. Track which issues the LLM hits and use them to write the WSL setup guide.

## Teardown

```powershell
wsl --terminate Debian
# Or destroy the Windows VM
```
