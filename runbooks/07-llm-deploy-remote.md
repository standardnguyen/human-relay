# Runbook 07: LLM-Driven Deploy — Remote Machine

**Type:** Claude-control (LLM-driven)
**Trigger:** Pre-release, remote deployment validation
**Duration:** ~15 minutes

## Purpose

Test the "deploy to a remote server" scenario — the LLM is on Machine A and needs to deploy human-relay on Machine B via SSH. This is the most complex deployment path because the LLM has to reason about networking between two machines, SSH key management, and which IP addresses go where.

## Prerequisites

- **Machine A (controller):** Has `claude` CLI, SSH access to Machine B
- **Machine B (target):** Fresh Linux VM with Docker installed, SSH server running
- Both machines can reach each other over the network

## Setup

### 1. Provision Target VM

```bash
TARGET_IP="target-vm-ip"

# Ensure Docker is installed on target
ssh root@${TARGET_IP} "which docker" || {
  echo "Install Docker on target first"
  exit 1
}

# Clone repo on target
ssh root@${TARGET_IP} "git clone <repo-url> /opt/human-relay"

# Ensure SSH key exists on target (for relay's command execution)
ssh root@${TARGET_IP} "test -f /root/.ssh/id_ed25519 || ssh-keygen -t ed25519 -f /root/.ssh/id_ed25519 -N '' -q"
ssh root@${TARGET_IP} "grep -q -f /root/.ssh/id_ed25519.pub /root/.ssh/authorized_keys 2>/dev/null || cat /root/.ssh/id_ed25519.pub >> /root/.ssh/authorized_keys"
```

## Test Execution

### 2. Run the LLM Agent from Controller

```bash
claude --dangerously-skip-permissions \
  --print \
  --model claude-opus-4-6 \
  --prompt "$(cat <<'PROMPT'
You are on a controller machine. You have SSH access to a remote server at root@TARGET_IP.
The human-relay repo is cloned at /opt/human-relay on the remote server.
Docker and Docker Compose are installed on the remote server.

Your task: deploy human-relay on the REMOTE server (not locally).

Key challenge: you need to figure out networking.
- MHR_HOST_IP: this is the IP the relay container will SSH to for command execution.
  It should be the remote server's own IP as seen from inside the Docker container.
- The dashboard and MCP endpoints should be accessible from THIS machine (the controller).
- The SSH key in docker-compose.yml (/root/.ssh/id_ed25519) needs to exist on the remote server
  and be authorized for localhost SSH.

Requirements:
1. SSH into the remote server and configure .env
2. Start the stack via docker compose
3. From THIS machine, verify dashboard on TARGET_IP:8090
4. From THIS machine, submit a test command to TARGET_IP:8080 via MCP JSON-RPC
5. From THIS machine, approve the command via the web API
6. Confirm command executes and returns output

Use auth token "remote-deploy-test".
Print DEPLOY_SUCCESS or DEPLOY_FAILED.
PROMPT
)" 2>&1 | tee /tmp/llm-deploy-remote.log
```

### 3. Evaluate

```bash
grep -q "DEPLOY_SUCCESS" /tmp/llm-deploy-remote.log && echo "PASS" || echo "FAIL"
```

## Key Failure Points

| Issue | Detail |
|-------|--------|
| **MHR_HOST_IP set to controller IP** | Common mistake — the LLM confuses "the machine I'm SSHing from" with "the machine the container needs to reach". MHR_HOST_IP should be the *target's* IP or Docker bridge gateway. |
| **SSH key chicken-and-egg** | docker-compose.yml mounts `/root/.ssh/id_ed25519`. The key needs to exist on the target AND be in authorized_keys for localhost. LLMs sometimes try to generate a key inside the container instead. |
| **Firewall on target** | Ports 8080/8090 need to be open. Cloud VMs often have security groups blocking this. |
| **Origin header for CSRF** | When submitting approve/deny from the controller, the Origin header must match the target's web port. LLMs sometimes get this wrong. |

## Two-Machine Networking Diagram

```
Controller (Machine A)          Target (Machine B)
+-----------------+            +-------------------+
|  claude CLI     |--SSH------>| sshd              |
|                 |            |                   |
|  curl (test)    |--HTTP----->| :8080 MCP (SSE)   |
|                 |--HTTP----->| :8090 Web Dashboard|
+-----------------+            |                   |
                               |  Docker container |
                               |  +-------------+  |
                               |  | human-relay |  |
                               |  |  SSH -----+ |  |
                               |  +-----------|--+ |
                               |              v    |
                               |  host (MHR_HOST_IP)|
                               +-------------------+
```

## Teardown

Destroy the target VM.
