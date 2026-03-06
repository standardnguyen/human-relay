# Runbook 09: Nested VM + Container Isolation Tests

**Type:** Both (scripted + LLM-driven variants)
**Trigger:** Pre-release, security/isolation validation
**Duration:** ~20-30 minutes

## Purpose

human-relay's whole architecture involves multiple layers of isolation — an LLM agent runs inside a sandbox, talks to the relay via MCP, the relay runs in a Docker container inside an LXC on Proxmox, and commands execute via SSH into other containers or the host. This runbook tests that the layering actually works by building the full stack and verifying commands flow through every layer correctly.

This also validates the `exec_container` and `write_file` tools' routing logic — direct SSH vs. `pct exec` fallback.

## Architecture Under Test

```
Layer 0: Proxmox Host (bare metal)
  |
  +-- Layer 1: LXC Container (CTID 9010) — runs human-relay via Docker
  |     |
  |     +-- Layer 2: Docker container — human-relay binary
  |           |
  |           +-- SSH to Layer 0 (pct exec route)
  |           +-- SSH to Layer 1a (direct SSH route)
  |
  +-- Layer 1a: LXC Container (CTID 9011) — target container (has SSH)
  |
  +-- Layer 1b: LXC Container (CTID 9012) — target container (no SSH, pct-only)
```

## Setup

### 1. Provision All Containers

```bash
PROXMOX_HOST="your-proxmox-host"

# Relay container (Docker-in-LXC)
ssh root@${PROXMOX_HOST} "pct create 9010 local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst \
  --hostname hr-relay \
  --memory 2048 --cores 4 \
  --rootfs local-lvm:16 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp \
  --features nesting=1 --unprivileged 0 --start 1"

# Target with SSH access
ssh root@${PROXMOX_HOST} "pct create 9011 local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst \
  --hostname hr-target-ssh \
  --memory 512 --cores 1 \
  --rootfs local-lvm:4 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp \
  --start 1"

# Target without SSH (pct-only)
ssh root@${PROXMOX_HOST} "pct create 9012 local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst \
  --hostname hr-target-pct \
  --memory 512 --cores 1 \
  --rootfs local-lvm:4 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp \
  --start 1"

sleep 5

RELAY_IP=$(ssh root@${PROXMOX_HOST} "pct exec 9010 -- hostname -I" | awk '{print $1}')
TARGET_SSH_IP=$(ssh root@${PROXMOX_HOST} "pct exec 9011 -- hostname -I" | awk '{print $1}')
TARGET_PCT_IP=$(ssh root@${PROXMOX_HOST} "pct exec 9012 -- hostname -I" | awk '{print $1}')
```

### 2. Setup SSH on Target-SSH Container

```bash
ssh root@${PROXMOX_HOST} "pct exec 9011 -- bash -c '
  apt-get update && apt-get install -y openssh-server &&
  systemctl enable --now sshd
'"
```

### 3. Deploy human-relay on Relay Container

Follow [02-deploy-smoke-proxmox](02-deploy-smoke-proxmox.md) steps 2-4 for CTID 9010, with:
- `MHR_HOST_IP` set to the Proxmox host IP (for pct exec routing)

Also distribute SSH keys:
```bash
# Copy relay's SSH public key to target-ssh container
ssh root@${PROXMOX_HOST} "pct exec 9010 -- cat /root/.ssh/id_ed25519.pub" | \
  ssh root@${PROXMOX_HOST} "pct exec 9011 -- tee -a /root/.ssh/authorized_keys"
```

### 4. Register Containers

```bash
# Register target-ssh (has direct SSH)
curl -sf -X POST http://${RELAY_IP}:8080/message \
  -H "Content-Type: application/json" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"register_container\",\"arguments\":{\"ctid\":9011,\"ip\":\"${TARGET_SSH_IP}\",\"hostname\":\"hr-target-ssh\",\"has_relay_ssh\":true}}}"

# Register target-pct (no SSH, pct exec fallback)
curl -sf -X POST http://${RELAY_IP}:8080/message \
  -H "Content-Type: application/json" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"register_container\",\"arguments\":{\"ctid\":9012,\"ip\":\"${TARGET_PCT_IP}\",\"hostname\":\"hr-target-pct\",\"has_relay_ssh\":false}}}"
```

## Tests

### Test A: Direct Command Execution (host)

```bash
# Submit request_command
RESPONSE=$(curl -sf -X POST http://${RELAY_IP}:8080/message \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"request_command","arguments":{"command":"hostname","reason":"test host execution"}}}')

REQUEST_ID=$(echo "${RESPONSE}" | python3 -c "import sys,json; print(json.loads(json.loads(sys.stdin.read())['result']['content'][0]['text'])['request_id'])")

curl -sf -X POST "http://${RELAY_IP}:8090/api/requests/${REQUEST_ID}/approve" \
  -H "Authorization: Bearer changeme" -H "Origin: http://${RELAY_IP}:8090"

sleep 3
echo "=== Test A: Host execution ==="
curl -sf "http://${RELAY_IP}:8090/api/requests" -H "Authorization: Bearer changeme" | \
  python3 -c "import sys,json; [print(f'Status: {r[\"status\"]}, Output: {r.get(\"result\",{}).get(\"stdout\",\"\").strip()}') for r in json.loads(sys.stdin.read()) if r['id']=='${REQUEST_ID}']"
```

### Test B: exec_container via Direct SSH (CTID 9011)

```bash
RESPONSE=$(curl -sf -X POST http://${RELAY_IP}:8080/message \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"exec_container","arguments":{"ctid":9011,"command":"hostname","reason":"test direct SSH exec"}}}')

REQUEST_ID=$(echo "${RESPONSE}" | python3 -c "import sys,json; d=json.loads(json.loads(sys.stdin.read())['result']['content'][0]['text']); print(d['request_id']); print(f'Route: {d[\"route\"]}')")

# Approve and check — route should be "direct_ssh"
curl -sf -X POST "http://${RELAY_IP}:8090/api/requests/${REQUEST_ID}/approve" \
  -H "Authorization: Bearer changeme" -H "Origin: http://${RELAY_IP}:8090"

sleep 3
echo "=== Test B: exec_container via direct SSH ==="
# Expected: hostname = hr-target-ssh, route = direct_ssh
```

### Test C: exec_container via pct exec (CTID 9012)

```bash
RESPONSE=$(curl -sf -X POST http://${RELAY_IP}:8080/message \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":30,"method":"tools/call","params":{"name":"exec_container","arguments":{"ctid":9012,"command":"hostname","reason":"test pct exec fallback"}}}')

REQUEST_ID=$(echo "${RESPONSE}" | python3 -c "import sys,json; d=json.loads(json.loads(sys.stdin.read())['result']['content'][0]['text']); print(d['request_id']); print(f'Route: {d[\"route\"]}')")

# Approve and check — route should be "pct_exec"
curl -sf -X POST "http://${RELAY_IP}:8090/api/requests/${REQUEST_ID}/approve" \
  -H "Authorization: Bearer changeme" -H "Origin: http://${RELAY_IP}:8090"

sleep 3
echo "=== Test C: exec_container via pct exec ==="
# Expected: hostname = hr-target-pct, route = pct_exec
```

### Test D: write_file to Container via pct push (CTID 9012)

```bash
CONTENT_B64=$(echo "hello from nested test" | base64)

RESPONSE=$(curl -sf -X POST http://${RELAY_IP}:8080/message \
  -H "Content-Type: application/json" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":40,\"method\":\"tools/call\",\"params\":{\"name\":\"write_file\",\"arguments\":{\"ctid\":9012,\"path\":\"/tmp/nested-test.txt\",\"content_base64\":\"${CONTENT_B64}\",\"reason\":\"test pct push write\"}}}")

REQUEST_ID=$(echo "${RESPONSE}" | python3 -c "import sys,json; print(json.loads(json.loads(sys.stdin.read())['result']['content'][0]['text'])['request_id'])")

curl -sf -X POST "http://${RELAY_IP}:8090/api/requests/${REQUEST_ID}/approve" \
  -H "Authorization: Bearer changeme" -H "Origin: http://${RELAY_IP}:8090"

sleep 3

# Verify file landed
ssh root@${PROXMOX_HOST} "pct exec 9012 -- cat /tmp/nested-test.txt"
echo "=== Test D: write_file via pct push ==="
# Expected: "hello from nested test"
```

### Test E: Full Audit Trail

```bash
# Grab the audit log from the relay container
ssh root@${PROXMOX_HOST} "pct exec 9010 -- docker compose -f /opt/human-relay/docker-compose.yml exec -T human-relay cat /opt/human-relay/data/audit.log" | python3 -c "
import sys, json
events = [json.loads(line) for line in sys.stdin if line.strip()]
print(f'Total audit events: {len(events)}')
for e in events:
    print(f'  {e[\"event\"]:25s} req={e.get(\"request_id\",\"N/A\")[:12]}  fields={list(e.get(\"fields\",{}).keys())}')
"
```

## LLM-Driven Variant

Instead of running the tests above as scripts, give an LLM the full setup and ask it to:
1. Register the containers
2. Execute commands through each routing path
3. Verify the outputs match expected hostnames
4. Check the audit log for completeness

This tests whether an LLM can understand the container registry and routing concepts from the docs.

## Teardown

```bash
ssh root@${PROXMOX_HOST} "pct stop 9010 && pct destroy 9010 --force"
ssh root@${PROXMOX_HOST} "pct stop 9011 && pct destroy 9011 --force"
ssh root@${PROXMOX_HOST} "pct stop 9012 && pct destroy 9012 --force"
```

## Pass Criteria

- All four execution paths work (host, direct SSH, pct exec, pct push)
- Routing is correct (direct_ssh for has_relay_ssh=true, pct_exec/pct_push for false)
- Audit log captures all events across all routing paths
- No cross-contamination between layers (commands execute in the right container)
