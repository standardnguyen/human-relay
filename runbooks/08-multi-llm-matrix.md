# Runbook 08: Multi-LLM Deploy Matrix

**Type:** Claude-control (multi-provider)
**Trigger:** Pre-release, doc quality validation across models
**Duration:** ~30-60 minutes (parallel)

## Purpose

Run the same deploy task across multiple LLM providers to validate that the human-relay documentation isn't accidentally tuned for one model's quirks. If Claude can deploy it but GPT-4 can't, the docs are relying on Claude-specific reasoning patterns. If all models struggle at the same step, that step needs better docs.

## LLM Matrix

| Provider | Model ID | CLI / API | Notes |
|----------|----------|-----------|-------|
| Anthropic | `claude-opus-4-6` | `claude` CLI | Primary. Dangerous mode via `--dangerously-skip-permissions` |
| Anthropic | `claude-sonnet-4-6` | `claude` CLI | Faster, cheaper — tests if simpler models can follow the docs |
| OpenAI | `gpt-4o` | Custom harness | Needs wrapper script (see below) |
| OpenAI | `o3` | Custom harness | Reasoning model — may overthink simple steps |
| Google | `gemini-2.5-pro` | Custom harness | Different tool-use patterns |
| Meta | `llama-4-maverick` | Local or API | Open-weight baseline — tests floor of instruction quality |

## Architecture

Each LLM gets its own fresh, isolated VM. All VMs are provisioned identically. The LLM is given SSH access to its VM and the same prompt. Results are collected and compared.

```
Test Runner
  |
  +---> VM-1 (claude-opus-4-6)     ---> Deploy attempt ---> PASS/FAIL + log
  +---> VM-2 (claude-sonnet-4-6)   ---> Deploy attempt ---> PASS/FAIL + log
  +---> VM-3 (gpt-4o)              ---> Deploy attempt ---> PASS/FAIL + log
  +---> VM-4 (o3)                  ---> Deploy attempt ---> PASS/FAIL + log
  +---> VM-5 (gemini-2.5-pro)      ---> Deploy attempt ---> PASS/FAIL + log
  +---> VM-6 (llama-4-maverick)    ---> Deploy attempt ---> PASS/FAIL + log
  |
  +---> Compare results, generate report
```

## The Prompt (Same for All Models)

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
- Use auth token "matrix-test-TOKEN_ID" for MHR_AUTH_TOKEN.

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

## Harness for Non-Claude Models

For models that don't have a `claude`-like CLI with tool use, use a wrapper that:
1. Sends the prompt to the model's API
2. Extracts shell commands from the response
3. Executes them via SSH on the target VM
4. Feeds stdout/stderr back to the model as the next message
5. Loops until the model prints DEPLOY_SUCCESS or DEPLOY_FAILED (or a step limit is hit)

```python
#!/usr/bin/env python3
"""
llm_deploy_harness.py — Generic harness for LLM deploy tests.
Connects to any OpenAI-compatible API and drives SSH commands on a target.
"""

import subprocess
import sys
import os

# Pseudocode — implement with your preferred SDK
#
# model = sys.argv[1]       # e.g. "gpt-4o"
# target_ip = sys.argv[2]   # VM IP
# api_key = os.environ["LLM_API_KEY"]
#
# messages = [{"role": "user", "content": PROMPT.replace("TARGET_IP", target_ip)}]
#
# for step in range(MAX_STEPS):
#     response = call_llm(model, messages, api_key)
#     commands = extract_shell_commands(response)
#     for cmd in commands:
#         result = ssh_exec(target_ip, cmd)
#         messages.append({"role": "user", "content": f"Command output:\n{result}"})
#     if "DEPLOY_SUCCESS" in response or "DEPLOY_FAILED" in response:
#         break
```

## Evaluation Criteria

### Per-Model Report

For each model, capture:
1. **Result:** PASS or FAIL
2. **Steps taken:** Number of commands issued
3. **Time to completion:** Wall clock
4. **Confusion points:** What the model found unclear (self-reported)
5. **Failure mode:** If failed, which step and why
6. **Recovery attempts:** Did the model try to fix its own mistakes?

### Cross-Model Comparison

| Metric | Goal |
|--------|------|
| All models pass | Docs are model-agnostic |
| Same confusion points across models | Those doc sections need rewriting |
| One model fails, others pass | That model has a weakness, not a doc problem |
| All models fail at same step | That step has a critical doc gap |
| Step count variance < 2x | Docs don't require unusual reasoning |

### Scoring Matrix (Example Output)

```
Model               Result  Steps  Time    Confusion Points
claude-opus-4-6     PASS    8      3m      none
claude-sonnet-4-6   PASS    12     4m      MHR_HOST_IP unclear
gpt-4o              PASS    10     5m      SSH key mount
o3                  PASS    15     8m      overthought CSRF origin
gemini-2.5-pro      FAIL    20     10m     couldn't figure out MCP JSON-RPC format
llama-4-maverick    FAIL    25     12m     gave up on SSH key setup
```

## Acting on Results

- If a model reports a confusion point: update the README/docs
- If a model fails at a specific step: add an explicit example or callout
- If all models struggle with MHR_HOST_IP: add a "what should I set this to?" decision tree
- If non-Claude models can't figure out MCP JSON-RPC: add a curl example to the README
- Track improvements over time — re-run the matrix after doc changes

## Teardown

Destroy all VMs. Keep logs in `/tmp/llm-matrix-{model}-{timestamp}.log`.
