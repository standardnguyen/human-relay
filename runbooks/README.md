# human-relay Runbooks

> **Status: Untested drafts.** These runbooks were written by Claude as a best-effort
> interpretation of how to validate human-relay deployments. None of them have been
> executed end-to-end. Expect wrong assumptions, missing steps, and broken commands.
> If you run one and it works, great. If not, fixes are welcome.

Test runbooks for validating human-relay deployments across environments, with both manual and LLM-driven (claude-control) test scenarios.

## Philosophy

human-relay sits between sandboxed AI agents and raw host access. The whole point is that an LLM should be able to read the deploy instructions, stand up the stack, and use it — with a human approving dangerous operations. If the deploy docs are confusing to an LLM, they're confusing to a human too.

These runbooks validate that by:
1. Spinning up fresh, ephemeral environments (VMs and containers)
2. Giving LLMs the deploy docs and seeing if they can get a working stack
3. Running functional tests against the deployed stack
4. Doing this across multiple LLM providers to catch instruction ambiguity

## Runbook Index

| Runbook | Type | Description |
|---------|------|-------------|
| [01-unit-tests](01-unit-tests.md) | Non-control | Standard Go unit + integration tests |
| [02-deploy-smoke-proxmox](02-deploy-smoke-proxmox.md) | Non-control | Scripted deploy to a fresh Proxmox LXC |
| [03-deploy-smoke-docker-local](03-deploy-smoke-docker-local.md) | Non-control | Scripted deploy via Docker Compose on a fresh VM |
| [04-llm-deploy-proxmox](04-llm-deploy-proxmox.md) | Claude-control | LLM reads docs and deploys to Proxmox LXC |
| [05-llm-deploy-docker](05-llm-deploy-docker.md) | Claude-control | LLM reads docs and deploys via Docker Compose |
| [06-llm-deploy-wsl](06-llm-deploy-wsl.md) | Claude-control | LLM reads docs and deploys inside WSL |
| [07-llm-deploy-remote](07-llm-deploy-remote.md) | Claude-control | LLM reads docs and deploys to a remote machine |
| [08-multi-llm-matrix](08-multi-llm-matrix.md) | Claude-control | Same deploy tasks across multiple LLM providers |
| [09-nested-vm-container](09-nested-vm-container.md) | Both | Multi-layer VM + container isolation tests |
| [10-merge-gate-ci](10-merge-gate-ci.md) | CI | On-merge matrix test across fresh machines + LLMs |

## Terminology

- **Non-control**: Traditional scripted tests. A shell script or CI step runs deterministic commands.
- **Claude-control / LLM-control**: An LLM agent is given the README/docs and told to deploy. The test measures whether the docs are good enough for an LLM to follow without human hand-holding. Uses "dangerous claude" (no sandbox restrictions) inside a disposable VM so the LLM has real root access to break things.
- **Multi-LLM**: The same control test run across Claude, GPT-4, Gemini, Llama, etc. to validate that the instructions aren't accidentally tuned for one model's quirks.

## Environment Requirements

- Proxmox host with API access (for LXC-based tests)
- Cloud provider credentials or local hypervisor (for ephemeral VM provisioning)
- API keys for target LLMs (see [08-multi-llm-matrix](08-multi-llm-matrix.md))
- Docker installed on test runners
- `claude` CLI with `--dangerously-skip-permissions` support (for claude-control tests)

## Known Issues

- Template names in runbooks (e.g. `debian-12-standard_12.12-1_amd64.tar.zst`) will drift as Proxmox publishes new versions. Check `pveam available --section system` for the current name.
- The `pct exec -- bash -c "..."` pattern can mangle PATH and shell variables depending on the SSH chain. If commands fail with "not found", try pushing a script into the container and executing it directly.
- Runbook 10 references `runbooks/prompts/deploy-test.txt` and `runbooks/harness/llm_deploy_harness.py` — these files have not been created yet.
- The LLM-driven runbooks (04-08) require a `claude`-like CLI or custom harness that does not exist for non-Anthropic models.
