# Autonomous Task: Fix Remaining Playwright Failures

**Created:** 2026-03-07
**Purpose:** Finish the e2e test suite for human-relay. An auditing Claude will review your work when you're done.

## Session Log

**Append your progress here as you work. This is the primary artifact the auditing Claude will review.**

```
--- SESSION LOG START ---


--- SESSION LOG END ---
```

## Context

PR #25 on the human-relay repo introduced:
- `MHR_SSH_CONFIG` env var for SSH config injection
- Shell mode quoting fix for exec_container (direct SSH removes `sh -c`; pct exec uses `shellQuote()`)
- Firefox SSE fix (initial `: connected` comment)
- Playwright e2e infrastructure with 40 golden screenshots

All Go tests pass (unit + integration + container e2e = 75 tests). But 6 of 42 Playwright tests fail.

## Current Failures (run `make test-e2e-frontend` to reproduce)

### Failure 1: [chromium] error request (non-zero exit) — screenshot diff
### Failure 2: [chromium] pending filter shows only pending — screenshot diff
### Failure 3: [chromium] non-shell command shows cmd-token and arg-tokens — screenshot diff
### Failure 4: [chromium] long command shows line count badge — screenshot diff

These 4 are **screenshot diffs caused by accumulated server state**. The relay process is shared across all tests (singleton in fixtures.ts). As tests create requests, later tests see more cards than the golden screenshots expected.

**Likely fix:** Re-generate golden screenshots with `make test-e2e-frontend-update`. But first make sure the whitelist button test (Failure 5/6) is fixed, otherwise its screenshot will be wrong.

### Failure 5: [chromium] clicking whitelist button adds rule and shows Whitelisted
### Failure 6: [firefox] clicking whitelist button adds rule and shows Whitelisted

Both fail at line 145 — `.btn-whitelist.active` not found after clicking the whitelist button.

**Root cause hypothesis:** The test at dashboard.spec.ts:129 does this sequence:
1. Submit a command, approve it, wait for complete
2. Open dashboard
3. Register a `page.on('dialog')` handler to auto-accept the confirm
4. Click `.btn-whitelist:not(.active).first()`
5. Wait 500ms
6. Expect `.btn-whitelist.active` to be visible

The issue is likely that after `addWhitelist()` calls `fetchWhitelist()` and `render()`, the DOM updates but Playwright's locator doesn't re-resolve. Or there's a timing issue. Check the actual DOM state after the click.

**Debugging approach:**
1. Add `await page.waitForSelector('.btn-whitelist.active', { timeout: 5000 })` instead of `waitForTimeout(500)` — if it times out, the class is never being set
2. Check if `fetchWhitelist()` is actually returning the new rule — add a `page.evaluate(() => whitelistRules)` call to inspect state
3. Check if `isWhitelisted()` matching is the issue — the command/args may not match exactly (e.g., `mark-for-wl` vs `["mark-for-wl"]`)
4. Consider that prior tests in the "Whitelist Panel" describe block (lines 110-117) call `relay.addWhitelist('echo', ['hello'])` via the MCP API — this may not trigger the frontend `fetchWhitelist()` since it bypasses the UI

**Another possibility:** The `confirm()` dialog handler is registered AFTER `openDashboard` — it's possible the click triggers the confirm before the handler is set up. Try registering the dialog handler BEFORE the click (it's already there, but verify it's not a race).

## Tasks (in order)

### Task 1: Fix the whitelist button test

Fix dashboard.spec.ts so "clicking whitelist button adds rule and shows Whitelisted" passes in both Chromium and Firefox. The Go backend code and HTML template are probably fine — this is likely a test timing/assertion issue.

Key files:
- `e2e/frontend/tests/dashboard.spec.ts` (lines 129-147)
- `web/templates/index.html` (lines 641-645 for render, 747-762 for addWhitelist function)

### Task 2: Re-generate all golden screenshots

Once the whitelist test passes functionally:
```bash
cd /root/human-relay
make test-e2e-frontend-update
```

This regenerates ALL golden screenshots to match the current accumulated state.

### Task 3: Verify all 42 tests pass

```bash
make test-e2e-frontend
```

All 42 tests (21 Chromium + 21 Firefox) should pass. If any still fail, fix them.

### Task 4: Verify Go tests still pass

```bash
make test
```

This runs unit + integration. Should be 75+ tests all passing.

### Task 5: Commit and push

Commit to the `dev` branch. PR #25 already exists and tracks `dev`. Message should reference the fix.

```bash
git add -A e2e/frontend/
git commit -m "Fix whitelist button e2e test + regenerate golden screenshots

- Fix timing issue in 'clicking whitelist button' test
- Regenerate all 40 golden screenshots for accumulated state
- All 42 Playwright tests pass (21 Chromium + 21 Firefox)

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>"
git push origin dev
```

### Task 6 (optional, if time permits): Investigate CTID 131 SSH

The relay on CTID 131 can't SSH to containers after a backup restore (host key mismatch). This is a production issue but not blocking the test suite. If you have time:
```bash
# From the wiki, check what's known
cat /root/personal-wiki/infrastructure/services/human-relay/overview.md | grep -A5 "131\|SSH\|key"
```

## Important Notes

- **Do NOT modify Go source code** (mcp/tools.go, main.go, web/handler.go, etc.) — those are correct and tested. Only modify test files.
- **Do NOT modify web/templates/index.html** unless you discover a genuine frontend bug (not a test issue).
- **Kill orphaned processes** before running tests: `ps aux | grep human-relay | grep -v grep | awk '{print $2}' | xargs kill 2>/dev/null; true`
- The Makefile uses `$(abspath $(BIN))` for the binary path — always run from `/root/human-relay/`.
- Tests use fixed ports 38080 (MCP) and 38090 (web). Kill anything on those ports before running.
- `workers: 1` in playwright.config.ts — tests run sequentially within each browser project, but Chromium runs before Firefox.

## When You're Done

Append a final summary to the session log section above. Include:
- What you changed and why
- Full test output (pass counts)
- Any open items remaining
- The git commit SHA

The auditing Claude will read this document + the git diff to verify your work.
