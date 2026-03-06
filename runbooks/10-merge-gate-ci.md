# Runbook 10: Merge Gate CI — Multi-LLM Matrix on Fresh Machines

**Type:** CI (automated on merge)
**Trigger:** Every merge to `main`
**Duration:** ~15-30 minutes (parallelized)

## Purpose

On every merge to main, spin up a fleet of fresh, ephemeral machines and run deploy tests across multiple LLMs in parallel. This is the final gate — if the docs regress and an LLM can no longer deploy from scratch, the merge gate catches it before release.

## Design Principles

1. **Every test gets a fresh machine.** No shared state, no leftover config. Each VM is provisioned from scratch and destroyed after.
2. **Multiple LLMs run in parallel.** Don't wait for Claude to finish before starting GPT-4. Spin up N VMs, run N tests simultaneously.
3. **Deterministic provisioning, non-deterministic deployment.** The VM setup is scripted; the deployment is LLM-driven. The test measures whether the LLM can follow the docs.
4. **Results are comparable.** Same prompt, same VM image, same starting state. Only the LLM varies.

## CI Workflow

### Forgejo Actions Workflow

```yaml
name: LLM Deploy Matrix
on:
  push:
    branches: [main]

jobs:
  # Standard tests first — fast gate
  unit-tests:
    runs-on: docker
    container:
      image: golang:1.24-bookworm
    steps:
      - uses: actions/checkout@v4
      - run: go build -o human-relay .
      - run: HUMAN_RELAY_BIN=${{ github.workspace }}/human-relay go test -v -count=1 ./integration/...

  # Provision VMs and run LLM deploy tests in parallel
  provision-vms:
    needs: unit-tests
    runs-on: self-hosted
    outputs:
      vm_ips: ${{ steps.provision.outputs.vm_ips }}
    steps:
      - name: Provision ephemeral VMs
        id: provision
        run: |
          # Provision one VM per LLM model
          MODELS="claude-opus-4-6 claude-sonnet-4-6 gpt-4o gemini-2.5-pro"
          VM_IPS=""

          for MODEL in ${MODELS}; do
            CTID=$((9100 + RANDOM % 100))

            pct create ${CTID} local:vztmpl/debian-12-standard_12.12-1_amd64.tar.zst \
              --hostname "hr-matrix-${MODEL}" \
              --memory 2048 --cores 4 \
              --rootfs local-lvm:16 \
              --net0 name=eth0,bridge=vmbr0,ip=dhcp \
              --features nesting=1 --unprivileged 0 --start 1

            sleep 5

            IP=$(pct exec ${CTID} -- hostname -I | awk '{print $1}')

            # Install Docker, clone repo
            pct exec ${CTID} -- bash -c "
              apt-get update &&
              apt-get install -y ca-certificates curl gnupg git openssh-server &&
              systemctl enable --now sshd &&
              install -m 0755 -d /etc/apt/keyrings &&
              curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg &&
              echo 'deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian bookworm stable' > /etc/apt/sources.list.d/docker.list &&
              apt-get update &&
              apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin &&
              ssh-keygen -t ed25519 -f /root/.ssh/id_ed25519 -N '' -q &&
              cat /root/.ssh/id_ed25519.pub >> /root/.ssh/authorized_keys
            "

            # Clone the repo at the commit being tested
            pct push ${CTID} ${{ github.workspace }}/.git /tmp/repo.git
            pct exec ${CTID} -- bash -c "git clone /tmp/repo.git /opt/human-relay"

            VM_IPS="${VM_IPS}${MODEL}:${IP}:${CTID},"
          done

          echo "vm_ips=${VM_IPS}" >> $GITHUB_OUTPUT

  llm-deploy-claude-opus:
    needs: provision-vms
    runs-on: self-hosted
    steps:
      - name: Extract VM IP
        id: vm
        run: |
          echo "${{ needs.provision-vms.outputs.vm_ips }}" | tr ',' '\n' | grep claude-opus-4-6 | cut -d: -f2
      - name: Run LLM deploy test
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
        run: |
          VM_IP=$(echo "${{ needs.provision-vms.outputs.vm_ips }}" | tr ',' '\n' | grep claude-opus-4-6 | cut -d: -f2)
          claude --dangerously-skip-permissions --print --model claude-opus-4-6 \
            --prompt "$(cat runbooks/prompts/deploy-test.txt | sed "s/TARGET_IP/${VM_IP}/g")" \
            2>&1 | tee /tmp/result-claude-opus.log
          grep -q "DEPLOY_SUCCESS" /tmp/result-claude-opus.log

  llm-deploy-claude-sonnet:
    needs: provision-vms
    runs-on: self-hosted
    steps:
      - name: Run LLM deploy test
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
        run: |
          VM_IP=$(echo "${{ needs.provision-vms.outputs.vm_ips }}" | tr ',' '\n' | grep claude-sonnet-4-6 | cut -d: -f2)
          claude --dangerously-skip-permissions --print --model claude-sonnet-4-6 \
            --prompt "$(cat runbooks/prompts/deploy-test.txt | sed "s/TARGET_IP/${VM_IP}/g")" \
            2>&1 | tee /tmp/result-claude-sonnet.log
          grep -q "DEPLOY_SUCCESS" /tmp/result-claude-sonnet.log

  llm-deploy-gpt4o:
    needs: provision-vms
    runs-on: self-hosted
    steps:
      - name: Run LLM deploy test
        env:
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
        run: |
          VM_IP=$(echo "${{ needs.provision-vms.outputs.vm_ips }}" | tr ',' '\n' | grep gpt-4o | cut -d: -f2)
          python3 runbooks/harness/llm_deploy_harness.py gpt-4o ${VM_IP} \
            2>&1 | tee /tmp/result-gpt4o.log
          grep -q "DEPLOY_SUCCESS" /tmp/result-gpt4o.log

  llm-deploy-gemini:
    needs: provision-vms
    runs-on: self-hosted
    steps:
      - name: Run LLM deploy test
        env:
          GOOGLE_API_KEY: ${{ secrets.GOOGLE_API_KEY }}
        run: |
          VM_IP=$(echo "${{ needs.provision-vms.outputs.vm_ips }}" | tr ',' '\n' | grep gemini-2.5-pro | cut -d: -f2)
          python3 runbooks/harness/llm_deploy_harness.py gemini-2.5-pro ${VM_IP} \
            2>&1 | tee /tmp/result-gemini.log
          grep -q "DEPLOY_SUCCESS" /tmp/result-gemini.log

  # Collect results and clean up
  report:
    needs: [llm-deploy-claude-opus, llm-deploy-claude-sonnet, llm-deploy-gpt4o, llm-deploy-gemini]
    if: always()
    runs-on: self-hosted
    steps:
      - name: Generate report
        run: |
          echo "## LLM Deploy Matrix Results"
          echo ""
          echo "| Model | Result |"
          echo "|-------|--------|"
          for LOG in /tmp/result-*.log; do
            MODEL=$(basename $LOG .log | sed 's/result-//')
            if grep -q "DEPLOY_SUCCESS" $LOG 2>/dev/null; then
              echo "| ${MODEL} | PASS |"
            else
              REASON=$(grep "DEPLOY_FAILED" $LOG 2>/dev/null | head -1 || echo "unknown")
              echo "| ${MODEL} | FAIL: ${REASON} |"
            fi
          done

      - name: Cleanup VMs
        if: always()
        run: |
          for ENTRY in $(echo "${{ needs.provision-vms.outputs.vm_ips }}" | tr ',' ' '); do
            CTID=$(echo $ENTRY | cut -d: -f3)
            [ -n "$CTID" ] && pct stop $CTID 2>/dev/null && pct destroy $CTID --force 2>/dev/null
          done
```

## The Deploy Test Prompt

Save as `runbooks/prompts/deploy-test.txt`:

```
You have SSH access to a fresh Debian 12 VM at root@TARGET_IP.
Docker and Docker Compose v2 are pre-installed.
The human-relay project is cloned at /opt/human-relay.

Your task: deploy this project using Docker Compose so that it is fully functional.

Rules:
- Use ONLY the files in /opt/human-relay to figure out what to do.
  Start with README.md, .env.example, docker-compose.yml, and Dockerfile.
- Do NOT look at Go source code. You are a user, not a developer.
- Do NOT ask for human input. Make all decisions yourself.
- Use "matrix-test" as the MHR_AUTH_TOKEN value.

Success criteria:
1. The web dashboard is accessible on port 8090
2. The MCP SSE endpoint is accessible on port 8080
3. You can submit a command via MCP JSON-RPC, approve it via the web API,
   and the command executes with correct output

When done, print exactly one of:
  DEPLOY_SUCCESS
  DEPLOY_FAILED: <one-line reason>

Then print a section called STEPS_TAKEN listing every significant action you took,
and a section called CONFUSION_POINTS listing anything in the docs that was unclear.
```

## Cost Estimation

Per run (4 models):
- **VMs:** 4 LXC containers, ~2GB RAM each, ~15 min lifespan = negligible on self-hosted Proxmox
- **Claude Opus:** ~$0.50-2.00 per deploy attempt (depends on retries)
- **Claude Sonnet:** ~$0.10-0.50
- **GPT-4o:** ~$0.20-1.00
- **Gemini:** ~$0.10-0.50
- **Total per merge:** ~$1-4

This is cheap enough to run on every merge. If costs become a concern, gate the LLM tests behind a `[deploy-test]` commit message tag or run them nightly instead.

## Scaling

To add a new LLM:
1. Add it to the `MODELS` list in `provision-vms`
2. Add a new `llm-deploy-{model}` job
3. Add API key to Forgejo secrets
4. If the model doesn't support `claude`-like CLI, add it to the harness

## Failure Response

- **One model fails:** Check that model's log. If the failure is model-specific (bad reasoning, gave up early), note it but don't block the merge.
- **Multiple models fail at the same step:** The docs have a regression. Block the merge, fix the docs, re-run.
- **All models fail:** Something is fundamentally broken — either the code or the docs. Investigate before merging.

## Relationship to Other Runbooks

This is the automated version of:
- [04-llm-deploy-proxmox](04-llm-deploy-proxmox.md) — but parallelized across models
- [08-multi-llm-matrix](08-multi-llm-matrix.md) — but running in CI instead of manually

The non-LLM tests ([01-unit-tests](01-unit-tests.md)) run first as a fast gate. If unit tests fail, LLM tests don't run.
