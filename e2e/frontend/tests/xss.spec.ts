import { test, expect, Page } from '@playwright/test';
import { ChildProcess, spawn } from 'child_process';
import { mkdtempSync, writeFileSync } from 'fs';
import { join } from 'path';
import { tmpdir } from 'os';
import http from 'http';

// Stored DOM-XSS regressions. An agent holds only an agent token, but the
// dashboard renders its strings while holding the APPROVER token in
// localStorage. Any script an agent smuggles into the page runs with the one
// credential that may approve. These tests submit attribute/JS-string breakout
// payloads with the agent token, open the page as the approver, sweep the
// mouse across the viewport, and assert that nothing ran and nothing was
// approved or whitelisted.
//
// This spec runs its own relay (approver token set) on ports of its own, so it
// does not share state with the fixture-driven specs.

const AGENT = 'xss-agent-token-throwaway';
const APPROVER = 'xss-approver-token-throwaway';
const MCP_PORT = 38180;
const WEB_PORT = 38190;
const WEB = `http://127.0.0.1:${WEB_PORT}`;

// Payload body: sets a marker, then whitelists `touch` using the page's own
// approver-bearing `headers` const. No single quotes (the old code escaped
// them) and no double quotes (they would end the attribute early).
const JS = 'window.__pwned=1;fetch(`/api/whitelist`,{method:`POST`,headers:{...headers,[`Content-Type`]:`application/json`},' +
  'body:JSON.stringify({command:`touch`,args:[`/tmp/pwned`]})});this.onmouseover=null';
const OVERLAY = 'position:fixed;left:0;top:0;width:100vw;height:100vh;opacity:0.01;z-index:99999';
const ATTR_BREAKOUT = (lead: string) => `${lead}" style="${OVERLAY}" onmouseover="${JS}" x="`;

let proc: ChildProcess | null = null;
let sseResp: http.IncomingMessage | null = null;
let endpoint = '';
const waiters: Array<(v: any) => void> = [];
let callID = 1;

function req(method: string, url: string, token: string, body?: any): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const data = body !== undefined ? JSON.stringify(body) : undefined;
    const r = http.request({
      hostname: u.hostname, port: u.port, path: u.pathname + u.search, method,
      headers: { Authorization: `Bearer ${token}`, ...(data ? { 'Content-Type': 'application/json' } : {}) },
    }, (res) => {
      const chunks: Buffer[] = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode!, body: Buffer.concat(chunks).toString() }));
    });
    r.on('error', reject);
    if (data) r.write(data);
    r.end();
  });
}

async function mcp(method: string, params?: any, notify = false): Promise<any> {
  const body: any = { jsonrpc: '2.0', method, params };
  let p: Promise<any> = Promise.resolve(null);
  if (!notify) {
    body.id = callID++;
    p = new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error('MCP timeout: ' + method)), 10_000);
      waiters.push((v) => { clearTimeout(t); resolve(v); });
    });
  }
  await req('POST', `http://127.0.0.1:${MCP_PORT}${endpoint}`, AGENT, body);
  return p;
}

async function tool(name: string, args: any): Promise<string> {
  const resp = await mcp('tools/call', { name, arguments: args });
  const text = resp.result.content[0].text;
  const id = JSON.parse(text).request_id;
  if (!id) throw new Error('no request_id: ' + text);
  return id;
}

test.describe.configure({ mode: 'serial' });

test.beforeAll(async () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pw-xss-'));
  writeFileSync(join(dataDir, 'whitelist.json'), '[]');
  const bin = process.env.HUMAN_RELAY_BIN;
  if (!bin) throw new Error('HUMAN_RELAY_BIN not set');
  proc = spawn(bin, [], {
    env: {
      ...process.env,
      MHR_MCP_PORT: String(MCP_PORT), MHR_WEB_PORT: String(WEB_PORT),
      MHR_AUTH_TOKEN: AGENT, MHR_APPROVER_TOKEN: APPROVER,
      MHR_DATA_DIR: dataDir,
      MHR_APPROVAL_COOLDOWN: '0', MHR_DEFAULT_TIMEOUT: '5', MHR_MAX_TIMEOUT: '10',
    },
    stdio: ['ignore', 'ignore', 'ignore'],
  });
  const deadline = Date.now() + 10_000;
  for (;;) {
    try { if ((await req('GET', `${WEB}/`, AGENT)).status === 200) break; } catch {}
    if (Date.now() > deadline) throw new Error('relay did not start');
    await new Promise((r) => setTimeout(r, 100));
  }
  endpoint = await new Promise<string>((resolve, reject) => {
    http.get(`http://127.0.0.1:${MCP_PORT}/sse`, { headers: { Authorization: `Bearer ${AGENT}` } }, (res) => {
      sseResp = res;
      let buf = '';
      res.on('data', (c: Buffer) => {
        buf += c.toString();
        const lines = buf.split('\n');
        buf = lines.pop()!;
        for (const line of lines) {
          if (!line.startsWith('data: ')) continue;
          const d = line.slice(6).trim();
          if (!endpoint && d.startsWith('/')) { endpoint = d; resolve(d); }
          else if (waiters.length) waiters.shift()!(JSON.parse(d));
        }
      });
    }).on('error', reject);
  });
  await mcp('initialize', { protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'xss', version: '1' } });
  await mcp('notifications/initialized', undefined, true);
});

test.afterAll(() => {
  sseResp?.destroy();
  proc?.kill('SIGKILL');
});

async function openAsApprover(page: Page, path: string) {
  const violations: string[] = [];
  await page.addInitScript((token) => {
    localStorage.setItem('mhr_token', token);
  }, APPROVER);
  page.on('pageerror', (e) => violations.push('pageerror: ' + e.message));
  page.on('console', (m) => { if (/Content Security Policy|Content-Security-Policy/i.test(m.text())) violations.push(m.text()); });
  await page.goto(WEB + path);
  await page.waitForTimeout(800);
  return violations;
}

async function sweepMouse(page: Page) {
  for (let x = 20; x < 1260; x += 150) {
    for (let y = 20; y < 880; y += 120) await page.mouse.move(x, y);
  }
  await page.waitForTimeout(600);
}

async function assertNothingRan(page: Page, pendingIDs: string[]) {
  expect(await page.evaluate(() => (window as any).__pwned)).toBeUndefined();
  const wl = await req('GET', `${WEB}/api/whitelist`, AGENT);
  expect(JSON.parse(wl.body)).toEqual([]);
  const all = JSON.parse((await req('GET', `${WEB}/api/requests`, AGENT)).body);
  for (const id of pendingIDs) {
    expect(all.find((r: any) => r.id === id)?.status).toBe('pending');
  }
}

test('dashboard: an attribute breakout in `command` runs nothing on hover', async ({ page }) => {
  const id = await tool('request_command_for_relay', { command: ATTR_BREAKOUT('ls'), args: [], reason: 'list files' });
  await openAsApprover(page, '/');
  await expect(page.locator('.request-card.pending')).toHaveCount(1);
  await sweepMouse(page);
  await assertNothingRan(page, [id]);
});

test('dashboard: Approve & Whitelist on quote- and &quot;-bearing args whitelists exactly those bytes and runs nothing else', async ({ page }) => {
  // Clear the earlier pending payloads out of the way by denying them as the approver.
  const all = JSON.parse((await req('GET', `${WEB}/api/requests`, AGENT)).body);
  for (const r of all.filter((r: any) => r.status === 'pending')) {
    await req('POST', `${WEB}/api/requests/${r.id}/deny`, APPROVER, { reason: 'cleanup' });
  }
  // The args path used to JSON-encode into the onclick attribute and turn `"`
  // into &quot;, so a literal &quot; in an arg decoded into a JS string end.
  const nastyArgs = [`&quot;]);window.__pwned=1;([&quot;`, `a"b'c\\d`, `</button><img src=x>`];
  const id = await tool('request_command_for_relay', { command: `echo`, args: nastyArgs, reason: 'quotes' });
  await openAsApprover(page, '/');
  const card = page.locator('.request-card.pending');
  await expect(card).toHaveCount(1);
  await card.locator('.btn-approve-wl').click();
  await expect.poll(async () => JSON.parse((await req('GET', `${WEB}/api/whitelist`, AGENT)).body).length).toBe(1);
  expect(await page.evaluate(() => (window as any).__pwned)).toBeUndefined();
  const wl = JSON.parse((await req('GET', `${WEB}/api/whitelist`, AGENT)).body);
  expect(wl[0].command).toBe('echo');
  expect(wl[0].args).toEqual(nastyArgs);
  const done = JSON.parse((await req('GET', `${WEB}/api/requests`, AGENT)).body).find((r: any) => r.id === id);
  expect(['approved', 'running', 'complete']).toContain(done.status);
});

test('dashboard: every control still works under the CSP, with no violations', async ({ page }) => {
  const violations = await openAsApprover(page, '/');
  // Static controls wired by addEventListener rather than onclick.
  await page.locator('.filters button[data-filter="whitelist"]').click();
  await expect(page.locator('.wl-rule')).toHaveCount(1);
  page.once('dialog', (d) => d.accept());
  await page.locator('.btn-wl-remove').first().click();
  await expect.poll(async () => JSON.parse((await req('GET', `${WEB}/api/whitelist`, AGENT)).body).length).toBe(0);
  await page.locator('.filters button[data-filter=""]').click();
  await page.locator('#turboBtn').click();
  await expect(page.locator('#turboBtn')).toHaveText('Turbo ON');
  await page.locator('#turboBtn').click();
  await expect(page.locator('#turboBtn')).toHaveText('Turbo');
  // A pending request: Deny through the prompt.
  const id = await tool('request_command_for_relay', { command: 'true', args: [], reason: 'deny me' });
  await expect(page.locator('.request-card.pending')).toHaveCount(1);
  page.once('dialog', (d) => d.accept('no'));
  await page.locator('.request-card.pending .btn-deny').click();
  await expect.poll(async () => JSON.parse((await req('GET', `${WEB}/api/requests`, AGENT)).body).find((r: any) => r.id === id).status).toBe('denied');
  expect(violations).toEqual([]);
});
