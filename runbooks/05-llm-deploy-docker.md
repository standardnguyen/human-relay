# Runbook 05: LLM-Driven Deploy — Docker Compose (Local)

**Type:** Claude-control (LLM-driven)
**Trigger:** Pre-release, doc quality validation
**Duration:** ~10 minutes

## Purpose

Give an LLM agent a fresh VM with Docker pre-installed and the human-relay repo, then see if it can get a working stack from just the README. This is the "easy mode" deploy path — Docker is already there, the LLM just needs to configure and start.

## Prerequisites

- Fresh VM (cloud instance, local QEMU, etc.) with:
  - Docker + Docker Compose v2 installed
  - Git installed
  - SSH access as root
- `claude` CLI on the test runner
- Repo cloned at `/opt/human-relay` on the target VM

## Setup

### 1. Provision VM

```bash
# Example with cloud-init or your preferred provisioner
# The VM needs: Docker, git, openssh-server, and the repo cloned
TARGET_IP="vm-ip-here"

ssh root@${TARGET_IP} "git clone <repo-url> /opt/human-relay"
```

### 2. Generate SSH Key on Target (for command execution)

```bash
ssh root@${TARGET_IP} "ssh-keygen -t ed25519 -f /root/.ssh/id_ed25519 -N '' -q && cat /root/.ssh/id_ed25519.pub >> /root/.ssh/authorized_keys"
```

## Test Execution

### 3. Run the LLM Agent

```bash
claude --dangerously-skip-permissions \
  --print \
  --model claude-opus-4-6 \
  --prompt "$(cat <<'PROMPT'
You have SSH access to a fresh Debian 12 VM at root@TARGET_IP.
Docker and Docker Compose are already installed.
The human-relay repo is cloned at /opt/human-relay.

Your task: get human-relay running using Docker Compose.

Use ONLY the README.md, .env.example, docker-compose.yml, and Dockerfile in the repo.
Do not look at source code — pretend you're a user following the docs.

Requirements:
1. Configure .env (auth token: "docker-local-test", figure out the right MHR_HOST_IP)
2. Handle any SSH key requirements from docker-compose.yml
3. Start the stack
4. Verify dashboard on :8090 and MCP endpoint on :8080
5. Submit a test command, approve it, confirm execution

Print DEPLOY_SUCCESS or DEPLOY_FAILED.
PROMPT
)" 2>&1 | tee /tmp/llm-deploy-docker.log
```

### 4. Evaluate

```bash
grep -q "DEPLOY_SUCCESS" /tmp/llm-deploy-docker.log && echo "PASS" || echo "FAIL"
```

## Key Differences from Proxmox Test

- Docker is pre-installed — tests whether the README's Docker Compose instructions alone are sufficient
- No Proxmox-specific concepts (LXC, pct, nesting) — simpler failure surface
- `MHR_HOST_IP` needs to be the Docker bridge gateway (172.17.0.1) or the host's LAN IP — this is a common point of confusion

## What Failures Tell Us

| Failure Mode | What It Means |
|-------------|---------------|
| LLM sets MHR_HOST_IP to 127.0.0.1 | Docs don't explain that the container needs to reach the host via a routable IP |
| SSH key volume mount fails | docker-compose.yml hardcodes `/root/.ssh/id_ed25519` — docs should mention this |
| LLM tries to run binary directly instead of Docker | README has both methods, LLM picked the wrong one |
| Auth header wrong format | Bearer token usage example not clear enough |

## Teardown

Destroy the VM via your cloud provider or hypervisor.
