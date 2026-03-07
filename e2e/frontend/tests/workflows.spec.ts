import { test, expect, openDashboard } from './fixtures';

test.describe('Workflow: Approve', () => {
  test('request appears live, approve via UI, output shown', async ({ page, relay }) => {
    await openDashboard(page, relay);

    // Submit a command AFTER dashboard is open — tests SSE push
    const id = await relay.submitCommand('echo', ['workflow-approve'], 'workflow approve test');

    // Card should appear via SSE without reload
    const card = page.locator(`.request-card:has(.request-id:text("${id}"))`);
    await expect(card).toBeVisible({ timeout: 5000 });
    await expect(card).toHaveClass(/pending/);

    // Verify command text and reason are shown
    await expect(card.locator('.cmd-token')).toHaveText('echo');
    await expect(card.locator('.reason')).toContainText('workflow approve test');

    // Check pending count in status bar
    await expect(page.locator('#requestCount')).toContainText('pending');

    // Click approve on this specific card
    await card.locator('.btn-approve').click();

    // Card should transition to complete with output
    await expect(card).toHaveClass(/complete/, { timeout: 10000 });
    await expect(card.locator('.output')).toContainText('workflow-approve');
  });
});

test.describe('Workflow: Deny', () => {
  test('deny with reason — reason appears on card', async ({ page, relay }) => {
    await openDashboard(page, relay);

    const id = await relay.submitCommand('rm', ['-rf', '/tmp/test'], 'deny workflow test');
    const card = page.locator(`.request-card:has(.request-id:text("${id}"))`);
    await expect(card).toBeVisible({ timeout: 5000 });

    // Set up dialog handler to type a reason
    page.on('dialog', async (dialog) => {
      await dialog.accept('looks dangerous');
    });

    await card.locator('.btn-deny').click();

    // Card transitions to denied, reason visible
    await expect(card).toHaveClass(/denied/, { timeout: 5000 });
    await expect(card.locator('.deny-reason')).toContainText('looks dangerous');
  });

  test('cancel deny — request stays pending', async ({ page, relay }) => {
    await openDashboard(page, relay);

    const id = await relay.submitCommand('echo', ['cancel-deny-test'], 'cancel deny workflow');
    const card = page.locator(`.request-card:has(.request-id:text("${id}"))`);
    await expect(card).toBeVisible({ timeout: 5000 });

    // Dismiss the deny dialog (cancel)
    page.on('dialog', async (dialog) => {
      await dialog.dismiss();
    });

    await card.locator('.btn-deny').click();

    // Wait a moment to confirm nothing changed
    await page.waitForTimeout(500);

    // Request should still be pending
    await expect(card).toHaveClass(/pending/);
    await expect(card.locator('.btn-approve')).toBeVisible();
    await expect(card.locator('.btn-deny')).toBeVisible();
  });
});

test.describe('Workflow: Approve & Whitelist', () => {
  test('one-click approve + whitelist, then auto-approve', async ({ page, relay }) => {
    await openDashboard(page, relay);

    // Submit a command
    const id1 = await relay.submitCommand('echo', ['wl-workflow'], 'whitelist workflow test');
    const card1 = page.locator(`.request-card:has(.request-id:text("${id1}"))`);
    await expect(card1).toBeVisible({ timeout: 5000 });

    // Click "Approve & Whitelist"
    await card1.locator('.btn-approve-wl').click();

    // Card should complete
    await expect(card1).toHaveClass(/complete/, { timeout: 10000 });

    // Whitelist panel should now show the new rule
    const rule = page.locator('.wl-rule', { hasText: 'wl-workflow' });
    await expect(rule).toBeVisible();

    // Submit the same command again — should auto-approve (skip pending)
    const id2 = await relay.submitCommand('echo', ['wl-workflow'], 'should auto-approve');
    // Wait for it to appear and be auto-completed
    const card2 = page.locator(`.request-card:has(.request-id:text("${id2}"))`);
    await expect(card2).toBeVisible({ timeout: 5000 });
    // Auto-approved commands go straight to complete (never pending)
    await expect(card2).toHaveClass(/complete/, { timeout: 10000 });
  });
});

test.describe('Workflow: Whitelist Management', () => {
  test('remove whitelist rule — command requires manual approval again', async ({ page, relay }) => {
    // Add a whitelist rule via API first
    await relay.addWhitelist('echo', ['wl-remove-test']);

    await openDashboard(page, relay);

    // Verify the rule exists in the panel
    const rule = page.locator('.wl-rule', { hasText: 'wl-remove-test' });
    await expect(rule).toBeVisible();

    // Accept the confirm dialog for removal
    page.on('dialog', async (dialog) => {
      await dialog.accept();
    });

    // Click remove on that rule
    await rule.locator('.btn-wl-remove').click();

    // Rule should disappear
    await expect(rule).not.toBeVisible({ timeout: 5000 });

    // Submit the same command — should now require manual approval
    const id = await relay.submitCommand('echo', ['wl-remove-test'], 'should be pending now');
    const card = page.locator(`.request-card:has(.request-id:text("${id}"))`);
    await expect(card).toBeVisible({ timeout: 5000 });
    await expect(card).toHaveClass(/pending/);
  });
});

test.describe('Workflow: Turbo Batch Session', () => {
  test('activate turbo, approve requests, deactivate', async ({ page, relay }) => {
    await relay.deactivateTurbo();
    await openDashboard(page, relay);

    // Activate turbo via UI button
    await page.click('#turboBtn');

    // Verify turbo is active
    await expect(page.locator('#turboBtn')).toHaveText('Turbo ON', { timeout: 5000 });
    await expect(page.locator('#turboBtn')).toHaveClass(/active/);
    await expect(page.locator('#turboInfo')).toHaveText(/\d+:\d{2}/);

    // Submit and approve a request
    const id1 = await relay.submitCommand('echo', ['turbo-batch-1'], 'turbo batch 1');
    const card1 = page.locator(`.request-card:has(.request-id:text("${id1}"))`);
    await expect(card1).toBeVisible({ timeout: 5000 });
    await card1.locator('.btn-approve').click();
    await expect(card1).toHaveClass(/complete/, { timeout: 10000 });

    // Submit another — cooldown should be short (turbo), wait for it
    const id2 = await relay.submitCommand('echo', ['turbo-batch-2'], 'turbo batch 2');
    const card2 = page.locator(`.request-card:has(.request-id:text("${id2}"))`);
    await expect(card2).toBeVisible({ timeout: 5000 });

    // Wait for the turbo cooldown to expire (10s from UI toggle)
    await expect(card2.locator('.btn-approve')).toBeEnabled({ timeout: 15000 });
    await card2.locator('.btn-approve').click();
    await expect(card2).toHaveClass(/complete/, { timeout: 10000 });

    // Deactivate turbo via UI
    await page.click('#turboBtn');
    await expect(page.locator('#turboBtn')).toHaveText('Turbo', { timeout: 5000 });
    await expect(page.locator('#turboBtn')).not.toHaveClass(/active/);
    await expect(page.locator('#turboInfo')).toBeEmpty();
  });
});

test.describe('Workflow: Shell Command Review', () => {
  test('shell command shows warnings, deny works', async ({ page, relay }) => {
    await openDashboard(page, relay);

    const id = await relay.submitCommand('rm -rf /tmp/dangerous', [], 'shell review test', { shell: true });
    const card = page.locator(`.request-card:has(.request-id:text("${id}"))`);
    await expect(card).toBeVisible({ timeout: 5000 });

    // Verify shell warnings
    await expect(card).toHaveClass(/shell-warn/);
    await expect(card.locator('.shell-badge')).toBeVisible();
    await expect(card.locator('.shell-warning-banner')).toBeVisible();

    // Deny it
    page.on('dialog', async (dialog) => {
      await dialog.accept('shell command too risky');
    });
    await card.locator('.btn-deny').click();

    await expect(card).toHaveClass(/denied/, { timeout: 5000 });
    await expect(card.locator('.deny-reason')).toContainText('shell command too risky');
  });
});

test.describe('Workflow: Live SSE Updates', () => {
  test('multiple requests appear in real-time without reload', async ({ page, relay }) => {
    await openDashboard(page, relay);

    // Submit 3 commands in sequence, verify each appears live
    const id1 = await relay.submitCommand('echo', ['live-1'], 'live update 1');
    const card1 = page.locator(`.request-card:has(.request-id:text("${id1}"))`);
    await expect(card1).toBeVisible({ timeout: 5000 });

    const id2 = await relay.submitCommand('echo', ['live-2'], 'live update 2');
    const card2 = page.locator(`.request-card:has(.request-id:text("${id2}"))`);
    await expect(card2).toBeVisible({ timeout: 5000 });

    const id3 = await relay.submitCommand('echo', ['live-3'], 'live update 3');
    const card3 = page.locator(`.request-card:has(.request-id:text("${id3}"))`);
    await expect(card3).toBeVisible({ timeout: 5000 });

    // All three visible simultaneously
    await expect(card1).toBeVisible();
    await expect(card2).toBeVisible();
    await expect(card3).toBeVisible();

    // Approve one via API and verify it transitions live
    await relay.approve(id2);
    await expect(card2).toHaveClass(/complete/, { timeout: 10000 });

    // The other two should still be pending
    await expect(card1).toHaveClass(/pending/);
    await expect(card3).toHaveClass(/pending/);
  });
});

test.describe('Workflow: Failed Command Output', () => {
  test('approve failing command, read stderr and exit code', async ({ page, relay }) => {
    await openDashboard(page, relay);

    const id = await relay.submitCommand('ls', ['/nonexistent-workflow-test'], 'error output test');
    const card = page.locator(`.request-card:has(.request-id:text("${id}"))`);
    await expect(card).toBeVisible({ timeout: 5000 });

    // Approve it
    await card.locator('.btn-approve').click();

    // Should transition to error
    await expect(card).toHaveClass(/error/, { timeout: 10000 });

    // stderr should be visible with the error message
    await expect(card.locator('.output')).toBeVisible();
    await expect(card.locator('.output-label')).toBeVisible();
  });
});
