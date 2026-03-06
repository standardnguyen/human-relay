# Runbook 03: Scripted Deploy Smoke Test — Docker Compose (Local)

**Type:** Non-control (scripted)
**Trigger:** On demand, pre-release validation
**Duration:** ~5 minutes

## Purpose

Validate that human-relay deploys and functions correctly via Docker Compose on a standard Linux machine — no Proxmox, no LXC, just Docker. This is the simplest deployment path and the one most new users will try first.

## Prerequisites

- Fresh VM or machine with Docker + Docker Compose v2 installed
- Git installed
- SSH key pair at `/root/.ssh/id_ed25519` (or modify docker-compose.yml)

## Steps

### 1. Clone and Configure

```bash
WORKDIR=$(mktemp -d)
git clone <repo-url> ${WORKDIR}/human-relay && cd ${WORKDIR}/human-relay
cp .env.example .env
sed -i 's/changeme/local-smoke-token/' .env
echo "MHR_HOST_IP=172.17.0.1" >> .env  # Docker bridge gateway — lets container SSH back to host
```

**Note:** `MHR_HOST_IP` must be an IP the container can SSH to for command execution. On Docker's default bridge, `172.17.0.1` reaches the host. If the host doesn't have an SSH server, commands will fail at execution time (submission + approval still work).

### 2. Generate a Throwaway SSH Key (if needed)

```bash
if [ ! -f /root/.ssh/id_ed25519 ]; then
  ssh-keygen -t ed25519 -f /root/.ssh/id_ed25519 -N "" -q
  cat /root/.ssh/id_ed25519.pub >> /root/.ssh/authorized_keys
fi
```

### 3. Start the Stack

```bash
docker compose up -d --build
```

### 4. Health Check

```bash
sleep 10

# Container state
docker compose ps --format '{{.Service}} {{.State}}'

# Dashboard
curl -sf http://127.0.0.1:8090/ > /dev/null && echo "Dashboard: OK" || echo "Dashboard: FAIL"

# MCP endpoint
timeout 3 curl -sf http://127.0.0.1:8080/sse 2>&1 | head -1
echo "SSE: OK"
```

### 5. Functional Test

```bash
# Submit command
RESPONSE=$(curl -sf -X POST http://127.0.0.1:8080/message \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request_command","arguments":{"command":"echo","args":["local-smoke-ok"],"reason":"local smoke test"}}}')

REQUEST_ID=$(echo "${RESPONSE}" | python3 -c "import sys,json; print(json.loads(json.loads(sys.stdin.read())['result']['content'][0]['text'])['request_id'])")

# Approve
curl -sf -X POST "http://127.0.0.1:8090/api/requests/${REQUEST_ID}/approve" \
  -H "Authorization: Bearer local-smoke-token" \
  -H "Origin: http://127.0.0.1:8090"

sleep 3

# Check result
curl -sf "http://127.0.0.1:8090/api/requests" \
  -H "Authorization: Bearer local-smoke-token" | python3 -c "
import sys, json
for r in json.loads(sys.stdin.read()):
    if r['id'] == '${REQUEST_ID}':
        print(f\"Status: {r['status']}\")
        if r.get('result'):
            print(f\"Stdout: {r['result'].get('stdout', '').strip()}\")
            print(f\"Exit: {r['result'].get('exit_code', 'N/A')}\")
"
```

### 6. Verify Audit Log

```bash
docker compose exec -T human-relay cat /opt/human-relay/data/audit.log | python3 -m json.tool --no-ensure-ascii
```

### 7. Teardown

```bash
docker compose down -v
rm -rf ${WORKDIR}
```

## Pass Criteria

- Stack starts, dashboard serves on :8090, MCP on :8080
- Command approved and executed, output = "local-smoke-ok"
- Audit log has full lifecycle
- Clean teardown, no dangling volumes

## Common Failures

| Symptom | Cause | Fix |
|---------|-------|-----|
| Port conflict on 8080/8090 | Another service using the port | Change `MHR_MCP_PORT`/`MHR_WEB_PORT` in `.env` |
| SSH connection refused on execution | Host has no sshd, or `MHR_HOST_IP` wrong | Install openssh-server on host, or set correct IP |
| "Permission denied (publickey)" | SSH key not mounted or not in authorized_keys | Check docker-compose.yml volume mount, add pub key to host's authorized_keys |
| Container exits immediately | Missing `MHR_AUTH_TOKEN` | Ensure `.env` has a non-empty token |
