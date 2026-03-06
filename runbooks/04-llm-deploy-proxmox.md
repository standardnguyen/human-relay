# Runbook 04: LLM-Driven Deploy — Proxmox LXC

**Type:** Claude-control (LLM-driven)
**Trigger:** Pre-release, doc quality validation
**Duration:** ~15-20 minutes

## Purpose

Give an LLM agent the human-relay README and a fresh Proxmox LXC, then see if it can deploy a working stack using only the documentation. This tests whether the deploy instructions are clear enough for an automated agent to follow.

The LLM runs in "dangerous" mode (no sandbox, real root access) inside the disposable LXC, so it can actually install packages, write configs, and start services. The LXC is destroyed afterward regardless of outcome.

## Prerequisites

- Proxmox host with SSH access
- `claude` CLI installed on the test runner
- Fresh LXC provisioned (see provisioning below)
- The repo cloned inside the LXC

## Setup

### 1. Provision the LXC

```bash
CTID=9002
PROXMOX_HOST="your-proxmox-host"

ssh root@${PROXMOX_HOST} "pct create ${CTID} local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst \
  --hostname hr-llm-deploy \
  --memory 2048 \
  --cores 4 \
  --rootfs local-lvm:16 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp \
  --features nesting=1 \
  --unprivileged 0 \
  --start 1"

sleep 5
CT_IP=$(ssh root@${PROXMOX_HOST} "pct exec ${CTID} -- hostname -I" | awk '{print $1}')
```

### 2. Seed the LXC with Basics

The LLM should figure out Docker installation itself from the README, but we give it basic tools:

```bash
ssh root@${PROXMOX_HOST} "pct exec ${CTID} -- bash -c '
  apt-get update && apt-get install -y git curl openssh-server
  systemctl enable --now sshd
'"
```

### 3. Clone the Repo Inside the LXC

```bash
ssh root@${PROXMOX_HOST} "pct exec ${CTID} -- git clone <repo-url> /opt/human-relay"
```

## Test Execution

### 4. Run the LLM Agent

From the test runner (which has `claude` CLI and the API key):

```bash
claude --dangerously-skip-permissions \
  --print \
  --model claude-opus-4-6 \
  --prompt "$(cat <<'PROMPT'
You have SSH access to a fresh Debian 12 machine at root@TARGET_IP.

Your task: deploy the human-relay project from /opt/human-relay on that machine.
Use ONLY the README.md and any config files in the repo to figure out how.
The deployment should use Docker Compose.

Requirements:
1. Install any missing dependencies (Docker, etc.)
2. Configure .env appropriately (use auth token "llm-deploy-test", set MHR_HOST_IP to the machine's IP)
3. Start the stack with docker compose
4. Verify the dashboard is accessible on port 8090
5. Verify the MCP SSE endpoint is accessible on port 8080
6. Submit a test command via the MCP endpoint, approve it via the web API, and confirm it executes

Print DEPLOY_SUCCESS if everything works, DEPLOY_FAILED if not.
Do NOT ask for human input. Make all decisions yourself.
PROMPT
)" 2>&1 | tee /tmp/llm-deploy-proxmox.log
```

Replace `TARGET_IP` with `${CT_IP}`.

### 5. Evaluate

```bash
grep -q "DEPLOY_SUCCESS" /tmp/llm-deploy-proxmox.log && echo "PASS" || echo "FAIL"
```

### 6. Capture Diagnostics (on failure)

```bash
ssh root@${CT_IP} "docker compose -f /opt/human-relay/docker-compose.yml logs" > /tmp/llm-deploy-proxmox-docker.log 2>&1
ssh root@${CT_IP} "cat /opt/human-relay/.env" > /tmp/llm-deploy-proxmox-env.log 2>&1
```

Review what the LLM actually did:
- Did it install Docker correctly?
- Did it figure out `MHR_HOST_IP`?
- Did it handle the SSH key volume mount in docker-compose.yml?
- Did it understand the MCP JSON-RPC protocol for the functional test?

## Teardown

```bash
ssh root@${PROXMOX_HOST} "pct stop ${CTID} && pct destroy ${CTID} --force"
```

## Pass Criteria

- LLM successfully deploys a working stack with no human intervention
- Dashboard accessible, MCP endpoint functional
- A command was submitted, approved, and executed via the API
- The LLM printed DEPLOY_SUCCESS

## What Failures Tell Us

| Failure Mode | What It Means |
|-------------|---------------|
| LLM can't figure out Docker install | README doesn't explain prerequisites clearly enough |
| Wrong `MHR_HOST_IP` | README doesn't explain what this variable is for |
| SSH key mount issues | docker-compose.yml assumptions not documented |
| Can't figure out MCP protocol | Tool documentation or examples are unclear |
| Auth errors on web API | Bearer token usage not documented well |
| LLM gives up and asks for help | Instructions have an ambiguous fork that needs clarification |

## Variants

Run this same test with `--model` set to different providers to compare (see [08-multi-llm-matrix](08-multi-llm-matrix.md)).
