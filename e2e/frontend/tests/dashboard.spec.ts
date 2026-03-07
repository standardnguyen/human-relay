import { test, expect, openDashboard, RelayHelper } from './fixtures';

test.describe('Dashboard States', () => {
  test('empty state — no requests', async ({ page, relay }) => {
    await openDashboard(page, relay);
    await expect(page.locator('.empty')).toBeVisible();
    await expect(page).toHaveScreenshot('empty-state.png');
  });

  test('single pending request', async ({ page, relay }) => {
    const id = await relay.submitCommand('echo', ['hello', 'world'], 'test pending display');
    await openDashboard(page, relay);
    await page.waitForSelector('.request-card.pending');
    await expect(page).toHaveScreenshot('single-pending.png');
  });

  test('pending shell command shows SHELL badge and warning', async ({ page, relay }) => {
    await relay.submitCommand('echo hello | tr a-z A-Z', [], 'test shell warning', { shell: true });
    await openDashboard(page, relay);
    await page.waitForSelector('.shell-badge');
    await expect(page.locator('.shell-warning-banner')).toBeVisible();
    await expect(page).toHaveScreenshot('shell-warning.png');
  });

  test('approved and completed request', async ({ page, relay }) => {
    const id = await relay.submitCommand('echo', ['completed-test'], 'test completed display');
    await relay.approve(id);
    await relay.waitForComplete(id);

    await openDashboard(page, relay);
    await page.waitForSelector('.request-card.complete');
    await expect(page).toHaveScreenshot('completed-request.png');
  });

  test('denied request shows deny reason', async ({ page, relay }) => {
    const id = await relay.submitCommand('rm', ['-rf', '/'], 'test denied display');
    await relay.deny(id, 'too dangerous for production');

    await openDashboard(page, relay);
    await page.waitForSelector('.request-card.denied');
    await expect(page.locator('.deny-reason')).toContainText('too dangerous for production');
    await expect(page).toHaveScreenshot('denied-request.png');
  });

  test('error request (non-zero exit)', async ({ page, relay }) => {
    const id = await relay.submitCommand('false', [], 'test error display');
    await relay.approve(id);
    await relay.waitForComplete(id);

    await openDashboard(page, relay);
    await page.waitForSelector('.request-card.error');
    await expect(page).toHaveScreenshot('error-request.png');
  });

  test('multiple requests mixed statuses', async ({ page, relay }) => {
    // Create a mix of pending, complete, denied
    const pending = await relay.submitCommand('echo', ['still-waiting'], 'pending command');

    const complete = await relay.submitCommand('echo', ['done'], 'completed command');
    await relay.approve(complete);
    await relay.waitForComplete(complete);

    const denied = await relay.submitCommand('echo', ['nope'], 'denied command');
    await relay.deny(denied, 'not today');

    await openDashboard(page, relay);
    await page.waitForSelector('.request-card.pending');
    await page.waitForSelector('.request-card.complete');
    await page.waitForSelector('.request-card.denied');
    await expect(page).toHaveScreenshot('mixed-statuses.png');
  });
});

test.describe('Filters', () => {
  test('pending filter shows only pending', async ({ page, relay }) => {
    // Ensure we have at least one of each
    await relay.submitCommand('echo', ['filter-pending'], 'for filter test');
    const cid = await relay.submitCommand('echo', ['filter-complete'], 'for filter test');
    await relay.approve(cid);
    await relay.waitForComplete(cid);

    await openDashboard(page, relay);
    await page.click('.filters button[data-filter="pending"]');
    await page.waitForTimeout(200);

    // All visible cards should be pending
    const cards = page.locator('.request-card');
    const count = await cards.count();
    for (let i = 0; i < count; i++) {
      await expect(cards.nth(i)).toHaveClass(/pending/);
    }
    await expect(page).toHaveScreenshot('filter-pending.png');
  });

  test('complete filter shows only completed', async ({ page, relay }) => {
    await openDashboard(page, relay);
    await page.click('.filters button[data-filter="complete"]');
    await page.waitForTimeout(200);

    const cards = page.locator('.request-card');
    const count = await cards.count();
    for (let i = 0; i < count; i++) {
      await expect(cards.nth(i)).toHaveClass(/complete/);
    }
    await expect(page).toHaveScreenshot('filter-complete.png');
  });
});

test.describe('Whitelist Panel', () => {
  test('whitelist panel appears after adding a rule', async ({ page, relay }) => {
    await relay.addWhitelist('echo', ['hello']);

    await openDashboard(page, relay);
    await page.waitForSelector('#whitelistPanel:not([style*="display: none"])');
    await expect(page.locator('.wl-rule')).toBeVisible();
    await expect(page).toHaveScreenshot('whitelist-panel.png');
  });

  test('whitelist button shows on completed request', async ({ page, relay }) => {
    const id = await relay.submitCommand('echo', ['wl-btn-test'], 'whitelist button test');
    await relay.approve(id);
    await relay.waitForComplete(id);

    await openDashboard(page, relay);
    await page.waitForSelector('.btn-whitelist');
    await expect(page).toHaveScreenshot('whitelist-button.png');
  });

  test('clicking whitelist button adds rule and shows Whitelisted', async ({ page, relay }) => {
    const id = await relay.submitCommand('echo', ['mark-for-wl'], 'click whitelist test');
    await relay.approve(id);
    await relay.waitForComplete(id);

    await openDashboard(page, relay);
    // Auto-accept the confirm dialog that addWhitelist() triggers
    page.on('dialog', async (dialog) => {
      await dialog.accept();
    });
    // Find the whitelist button for this specific command and click it
    const btn = page.locator('.btn-whitelist:not(.active)').first();
    await btn.click();

    // After clicking, wait for the async whitelist flow (confirm → POST → fetchWhitelist → render)
    await page.waitForSelector('.btn-whitelist.active', { timeout: 5000 });
    await expect(page).toHaveScreenshot('whitelisted-state.png');
  });

  test('whitelist panel collapses on click', async ({ page, relay }) => {
    await openDashboard(page, relay);
    const panel = page.locator('#whitelistPanel');
    if (await panel.isVisible()) {
      await page.click('#whitelistPanel h2');
      await page.waitForTimeout(200);
      await expect(page.locator('#whitelistBody')).toBeHidden();
      await expect(page).toHaveScreenshot('whitelist-collapsed.png');
    }
  });
});

test.describe('Approval Interaction', () => {
  test('approve button works and shows result', async ({ page, relay }) => {
    const id = await relay.submitCommand('echo', ['approve-from-ui'], 'UI approval test');

    await openDashboard(page, relay);
    await page.waitForSelector('.request-card.pending');

    // Click approve
    await page.click('.btn-approve');

    // Wait for it to complete
    await page.waitForSelector('.request-card.complete', { timeout: 10_000 });
    await page.waitForTimeout(300);
    await expect(page).toHaveScreenshot('after-ui-approve.png');
  });

  test('deny button shows prompt result', async ({ page, relay }) => {
    const id = await relay.submitCommand('echo', ['deny-from-ui'], 'UI denial test');

    await openDashboard(page, relay);
    await page.waitForSelector('.request-card.pending');

    // Mock the prompt dialog
    page.on('dialog', async (dialog) => {
      await dialog.accept('rejected via UI test');
    });

    await page.click('.btn-deny');
    await page.waitForSelector('.request-card.denied', { timeout: 5_000 });
    await page.waitForTimeout(300);
    await expect(page.locator('.deny-reason', { hasText: 'rejected via UI test' })).toBeVisible();
    await expect(page).toHaveScreenshot('after-ui-deny.png');
  });
});

test.describe('Output Display', () => {
  test('stdout displayed in output block', async ({ page, relay }) => {
    const id = await relay.submitCommand('echo', ['line1\nline2\nline3'], 'multiline output test');
    await relay.approve(id);
    await relay.waitForComplete(id);

    await openDashboard(page, relay);
    await page.waitForSelector('.output');
    await expect(page.locator('.output').first()).toBeVisible();
    await expect(page).toHaveScreenshot('stdout-display.png');
  });

  test('stderr displayed for error commands', async ({ page, relay }) => {
    const id = await relay.submitCommand('ls', ['/nonexistent-dir-e2e'], 'stderr test');
    await relay.approve(id);
    await relay.waitForComplete(id);

    await openDashboard(page, relay);
    await page.waitForSelector('.request-card.error');
    await expect(page).toHaveScreenshot('stderr-display.png');
  });
});

test.describe('Status Bar', () => {
  test('connected indicator is green', async ({ page, relay }) => {
    await openDashboard(page, relay);
    const dot = page.locator('#connDot');
    await expect(dot).not.toHaveClass(/disconnected/);
    await expect(page.locator('#connStatus')).toHaveText('Connected');
    await expect(page.locator('.status-bar')).toHaveScreenshot('status-bar-connected.png');
  });

  test('request count shows pending count', async ({ page, relay }) => {
    await relay.submitCommand('echo', ['count-test-1'], 'count test');
    await relay.submitCommand('echo', ['count-test-2'], 'count test');

    await openDashboard(page, relay);
    await page.waitForTimeout(500);
    // Should show "N pending" where N >= 2
    const countText = await page.locator('#requestCount').textContent();
    expect(countText).toMatch(/\d+ pending/);
    await expect(page.locator('.status-bar')).toHaveScreenshot('status-bar-pending-count.png');
  });
});

test.describe('Command Display', () => {
  test('non-shell command shows cmd-token and arg-tokens', async ({ page, relay }) => {
    await relay.submitCommand('docker', ['compose', '-f', '/opt/app/docker-compose.yml', 'up', '-d'], 'arg token display test');
    await openDashboard(page, relay);
    await page.waitForSelector('.cmd-token');
    await expect(page.locator('.arg-token').first()).toBeVisible();
    await expect(page.locator('.request-card').last()).toHaveScreenshot('cmd-arg-tokens.png');
  });

  test('long command shows line count badge', async ({ page, relay }) => {
    const longCmd = 'line1\nline2\nline3\nline4\nline5\nline6\nline7';
    await relay.submitCommand(longCmd, [], 'long command test', { shell: true });
    await openDashboard(page, relay);
    await page.waitForSelector('.line-count-badge');
    await expect(page.locator('.request-card').last()).toHaveScreenshot('line-count-badge.png');
  });
});

// Helper: get the ordered list of request IDs visible on the page
async function getVisibleRequestIDs(page: any): Promise<string[]> {
  return page.locator('.request-id').allTextContents();
}

// Helper: get relative order of specific IDs within the visible list
function relativeOrder(allIDs: string[], targetIDs: string[]): string[] {
  return allIDs.filter(id => targetIDs.includes(id));
}

test.describe('Sort Order', () => {
  test('complete tab sorts newest first', async ({ page, relay }) => {
    // Create 3 requests with slight delays for distinct timestamps
    const ids: string[] = [];
    for (let i = 0; i < 3; i++) {
      const id = await relay.submitCommand('echo', [`sort-complete-${i}`], `sort complete ${i}`);
      ids.push(id);
      await new Promise(r => setTimeout(r, 100));
    }

    // Approve and complete all 3
    for (const id of ids) {
      await relay.approve(id);
      await relay.waitForComplete(id);
    }

    await openDashboard(page, relay);
    await page.click('.filters button[data-filter="complete"]');
    await page.waitForTimeout(300);

    const visible = await getVisibleRequestIDs(page);
    const order = relativeOrder(visible, ids);

    // Should be newest first: ids[2], ids[1], ids[0]
    expect(order).toEqual([ids[2], ids[1], ids[0]]);
    await expect(page).toHaveScreenshot('sort-complete-newest-first.png');
  });

  test('denied tab sorts newest first', async ({ page, relay }) => {
    const ids: string[] = [];
    for (let i = 0; i < 3; i++) {
      const id = await relay.submitCommand('echo', [`sort-denied-${i}`], `sort denied ${i}`);
      ids.push(id);
      await new Promise(r => setTimeout(r, 100));
    }

    for (const id of ids) {
      await relay.deny(id, `denied-sort-${id}`);
    }

    await openDashboard(page, relay);
    await page.click('.filters button[data-filter="denied"]');
    await page.waitForTimeout(300);

    const visible = await getVisibleRequestIDs(page);
    const order = relativeOrder(visible, ids);

    expect(order).toEqual([ids[2], ids[1], ids[0]]);
    await expect(page).toHaveScreenshot('sort-denied-newest-first.png');
  });

  test('error tab sorts newest first', async ({ page, relay }) => {
    const ids: string[] = [];
    for (let i = 0; i < 3; i++) {
      const id = await relay.submitCommand('false', [], `sort error ${i}`);
      ids.push(id);
      await new Promise(r => setTimeout(r, 100));
    }

    for (const id of ids) {
      await relay.approve(id);
      await relay.waitForComplete(id);
    }

    await openDashboard(page, relay);
    await page.click('.filters button[data-filter="error"]');
    await page.waitForTimeout(300);

    const visible = await getVisibleRequestIDs(page);
    const order = relativeOrder(visible, ids);

    expect(order).toEqual([ids[2], ids[1], ids[0]]);
    await expect(page).toHaveScreenshot('sort-error-newest-first.png');
  });

  test('pending tab sorts oldest first', async ({ page, relay }) => {
    const ids: string[] = [];
    for (let i = 0; i < 3; i++) {
      const id = await relay.submitCommand('echo', [`sort-pending-${i}`], `sort pending ${i}`);
      ids.push(id);
      await new Promise(r => setTimeout(r, 100));
    }

    await openDashboard(page, relay);
    await page.click('.filters button[data-filter="pending"]');
    await page.waitForTimeout(300);

    const visible = await getVisibleRequestIDs(page);
    const order = relativeOrder(visible, ids);

    // Should be oldest first: ids[0], ids[1], ids[2]
    expect(order).toEqual([ids[0], ids[1], ids[2]]);
    await expect(page).toHaveScreenshot('sort-pending-oldest-first.png');
  });

  test('all tab shows pending oldest-first then non-pending newest-first', async ({ page, relay }) => {
    // Create a controlled set: 2 pending, 1 complete, 1 denied
    const pending1 = await relay.submitCommand('echo', ['sort-all-p1'], 'sort all pending 1');
    await new Promise(r => setTimeout(r, 100));
    const complete1 = await relay.submitCommand('echo', ['sort-all-c1'], 'sort all complete 1');
    await new Promise(r => setTimeout(r, 100));
    const pending2 = await relay.submitCommand('echo', ['sort-all-p2'], 'sort all pending 2');
    await new Promise(r => setTimeout(r, 100));
    const denied1 = await relay.submitCommand('echo', ['sort-all-d1'], 'sort all denied 1');

    await relay.approve(complete1);
    await relay.waitForComplete(complete1);
    await relay.deny(denied1, 'sort test');

    await openDashboard(page, relay);
    // Click "All" filter (first button, should already be active but click to be sure)
    await page.click('.filters button[data-filter=""]');
    await page.waitForTimeout(300);

    const visible = await getVisibleRequestIDs(page);

    // Find positions of our test IDs
    const p1Idx = visible.indexOf(pending1);
    const p2Idx = visible.indexOf(pending2);
    const c1Idx = visible.indexOf(complete1);
    const d1Idx = visible.indexOf(denied1);

    // Pending items should come before non-pending
    expect(p1Idx).toBeLessThan(c1Idx);
    expect(p1Idx).toBeLessThan(d1Idx);
    expect(p2Idx).toBeLessThan(c1Idx);
    expect(p2Idx).toBeLessThan(d1Idx);

    // Pending items should be oldest first (pending1 before pending2)
    expect(p1Idx).toBeLessThan(p2Idx);

    // Non-pending items should be newest first (denied1 created after complete1)
    expect(d1Idx).toBeLessThan(c1Idx);

    await expect(page).toHaveScreenshot('sort-all-mixed.png');
  });
});
