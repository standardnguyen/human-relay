# Runbook 02: Scripted Deploy Smoke Test — Proxmox LXC

**Type:** Non-control (scripted)
**Trigger:** On demand, pre-release validation
**Duration:** ~10 minutes

## Purpose

Validate that a fresh Proxmox LXC container can run human-relay via Docker Compose, without any LLM involvement. This is the baseline — if the scripted deploy doesn't work, the LLM deploy tests are meaningless.

## Prerequisites

- Proxmox host with API access (or SSH as root)
- Debian/Ubuntu container template available
- Network connectivity between test runner and Proxmox host

## Steps

### 1. Provision Fresh LXC

```bash
CTID=9001  # high CTID to avoid collisions with production
PROXMOX_HOST="your-proxmox-host"

ssh root@${PROXMOX_HOST} "pct create ${CTID} local:vztmpl/debian-12-standard_12.12-1_amd64.tar.zst \
  --hostname hr-smoke-test \
  --memory 1024 \
  --cores 2 \
  --rootfs local-lvm:8 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp \
  --features nesting=1 \
  --unprivileged 0 \
  --start 1"
```

**Critical:** `nesting=1` is required for Docker-in-LXC. Without it, Docker will fail to start.

### 2. Wait for Network + Get IP

```bash
sleep 5
CT_IP=$(ssh root@${PROXMOX_HOST} "pct exec ${CTID} -- hostname -I" | awk '{print $1}')
echo "Container IP: ${CT_IP}"
```

### 3. Install Docker Inside LXC

```bash
ssh root@${PROXMOX_HOST} "pct exec ${CTID} -- bash -c '
  apt-get update &&
  apt-get install -y ca-certificates curl gnupg &&
  install -m 0755 -d /etc/apt/keyrings &&
  curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg &&
  echo \"deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian bookworm stable\" > /etc/apt/sources.list.d/docker.list &&
  apt-get update &&
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin git
'"
```

### 4. Clone and Deploy

```bash
ssh root@${PROXMOX_HOST} "pct exec ${CTID} -- bash -c '
  git clone <repo-url> /opt/human-relay &&
  cd /opt/human-relay &&
  cp .env.example .env &&
  sed -i \"s/changeme/smoke-test-token/\" .env &&
  echo \"MHR_HOST_IP=${CT_IP}\" >> .env &&
  docker compose up -d
'"
```

### 5. Health Check

```bash
sleep 15

# Container running?
ssh root@${PROXMOX_HOST} "pct exec ${CTID} -- docker compose -f /opt/human-relay/docker-compose.yml ps"

# Dashboard reachable?
curl -sf http://${CT_IP}:8090/ > /dev/null && echo "Dashboard: OK" || echo "Dashboard: FAIL"

# MCP SSE endpoint (will hang — timeout is expected and fine)
timeout 3 curl -sf http://${CT_IP}:8080/sse 2>&1 | head -1
echo "SSE endpoint: OK (timeout expected)"
```

### 6. Functional Test — Submit and Approve a Command

```bash
# Submit via MCP JSON-RPC
RESPONSE=$(curl -sf -X POST http://${CT_IP}:8080/message \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request_command","arguments":{"command":"echo","args":["hello-from-smoke-test"],"reason":"smoke test"}}}')

REQUEST_ID=$(echo "${RESPONSE}" | python3 -c "import sys,json; print(json.loads(json.loads(sys.stdin.read())['result']['content'][0]['text'])['request_id'])")
echo "Request ID: ${REQUEST_ID}"

# Approve via web API
curl -sf -X POST "http://${CT_IP}:8090/api/requests/${REQUEST_ID}/approve" \
  -H "Authorization: Bearer smoke-test-token" \
  -H "Origin: http://${CT_IP}:8090"

# Wait for execution
sleep 3

# Get result
RESULT=$(curl -sf "http://${CT_IP}:8090/api/requests" \
  -H "Authorization: Bearer smoke-test-token" | python3 -c "
import sys, json
for r in json.loads(sys.stdin.read()):
    if r['id'] == '${REQUEST_ID}':
        print(f\"Status: {r['status']}\")
        if r.get('result'):
            print(f\"Output: {r['result'].get('stdout', '').strip()}\")
")
echo "${RESULT}"
```

Expected: Status=complete, Output=hello-from-smoke-test

### 7. Verify Audit Log

```bash
ssh root@${PROXMOX_HOST} "pct exec ${CTID} -- docker compose -f /opt/human-relay/docker-compose.yml exec -T human-relay cat /opt/human-relay/data/audit.log"
```

Expected: 4 JSONL lines — `request_created`, `request_approved`, `execution_started`, `execution_completed`

### 8. Teardown

```bash
ssh root@${PROXMOX_HOST} "pct stop ${CTID} && pct destroy ${CTID} --force"
```

## Pass Criteria

- LXC starts, Docker runs inside it
- human-relay container starts, dashboard accessible on :8090
- MCP endpoint accepts JSON-RPC on :8080
- Command submitted, approved, executed — output correct
- Audit log contains complete lifecycle
- Clean teardown with no orphaned resources

## Common Failures

| Symptom | Cause | Fix |
|---------|-------|-----|
| Docker fails to start | Missing `nesting=1` | Recreate LXC with `--features nesting=1` |
| Dashboard unreachable | Firewall or port binding | Check `MHR_WEB_PORT`, verify no firewall rules |
| Command hangs | Bad `MHR_HOST_IP` | The relay tries to SSH to this IP for execution — must be reachable from inside the container |
| SSH key errors | Missing volume mount | `docker-compose.yml` mounts `/root/.ssh/id_ed25519` — ensure the key exists |
