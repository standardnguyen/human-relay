import { test as base, expect, Page } from '@playwright/test';
import { ChildProcess, spawn } from 'child_process';
import { mkdtempSync, writeFileSync } from 'fs';
import { join } from 'path';
import { tmpdir } from 'os';
import http from 'http';

const TOKEN = 'pw-test-token';

interface ServerInfo {
  mcpPort: number;
  webPort: number;
  token: string;
  dataDir: string;
  proc: ChildProcess;
}

let server: ServerInfo | null = null;
let callID = 1;
let sseSessionID: string | null = null;
let sseEventCh: Array<{ resolve: (v: any) => void }> = [];
let sseResp: http.IncomingMessage | null = null;

function nextCallID(): number {
  return callID++;
}

async function httpReq(method: string, url: string, body?: any, extraHeaders?: Record<string, string>): Promise<{ status: number; body: string; headers: http.IncomingHeaders }> {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const data = body ? JSON.stringify(body) : undefined;
    const req = http.request({
      hostname: u.hostname,
      port: u.port,
      path: u.pathname + u.search,
      method,
      headers: {
        ...(data ? { 'Content-Type': 'application/json' } : {}),
        ...extraHeaders,
      },
    }, (res) => {
      let chunks: Buffer[] = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode!, body: Buffer.concat(chunks).toString(), headers: res.headers }));
    });
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}

async function startRelay(): Promise<ServerInfo> {
  const bin = process.env.HUMAN_RELAY_BIN;
  if (!bin) throw new Error('HUMAN_RELAY_BIN not set');

  // Use fixed ports to avoid conflicts with orphaned processes
  const mcpPort = 38080;
  const webPort = 38090;
  const dataDir = mkdtempSync(join(tmpdir(), 'pw-relay-'));

  // Write empty whitelist
  writeFileSync(join(dataDir, 'whitelist.json'), '[]');

  const proc = spawn(bin, [], {
    env: {
      ...process.env,
      MHR_MCP_PORT: String(mcpPort),
      MHR_WEB_PORT: String(webPort),
      MHR_AUTH_TOKEN: TOKEN,
      MHR_DATA_DIR: dataDir,
      MHR_HOST_IP: '192.168.10.50',
      MHR_DEFAULT_TIMEOUT: '5',
      MHR_MAX_TIMEOUT: '10',
      MHR_APPROVAL_COOLDOWN: '0',
    },
    stdio: ['pipe', 'pipe', 'pipe'],
  });

  // Wait for ready
  const deadline = Date.now() + 10_000;
  let started = false;
  while (Date.now() < deadline) {
    try {
      const r = await httpReq('GET', `http://127.0.0.1:${webPort}/`);
      if (r.status === 200) { started = true; break; }
    } catch {}
    await new Promise(r => setTimeout(r, 100));
  }
  if (!started) {
    proc.kill();
    throw new Error(`Server failed to start on port ${webPort} within 10s`);
  }

  return { mcpPort, webPort, token: TOKEN, dataDir, proc };
}

async function connectSSE(mcpURL: string): Promise<string> {
  return new Promise((resolve, reject) => {
    http.get(`${mcpURL}/sse`, (res) => {
      sseResp = res;
      let buf = '';
      res.on('data', (chunk: Buffer) => {
        buf += chunk.toString();
        const lines = buf.split('\n');
        buf = lines.pop()!;
        for (const line of lines) {
          if (line.startsWith('data: ') && !sseSessionID) {
            sseSessionID = line.slice(6).trim();
            resolve(sseSessionID);
          } else if (line.startsWith('data: ') && sseSessionID) {
            // Response event
            const data = line.slice(6).trim();
            if (sseEventCh.length > 0) {
              const waiter = sseEventCh.shift()!;
              waiter.resolve(JSON.parse(data));
            }
          }
        }
      });
      res.on('error', reject);
    }).on('error', reject);
  });
}

async function mcpCall(method: string, params?: any): Promise<any> {
  const id = nextCallID();
  const body = { jsonrpc: '2.0', id, method, params };
  const url = `http://127.0.0.1:${server!.mcpPort}${sseSessionID}`;

  // Set up the SSE waiter BEFORE sending the request to avoid a race where
  // the SSE response arrives before httpReq resolves and the waiter is added.
  const responsePromise = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`MCP timeout for ${method}`)), 15_000);
    sseEventCh.push({
      resolve: (v: any) => { clearTimeout(timer); resolve(v); },
    });
  });

  await httpReq('POST', url, body);
  return responsePromise;
}

async function mcpNotify(method: string, params?: any): Promise<void> {
  const body = { jsonrpc: '2.0', method, params };
  const url = `http://127.0.0.1:${server!.mcpPort}${sseSessionID}`;
  await httpReq('POST', url, body);
}

function killServer() {
  if (server) {
    server.proc.kill('SIGKILL');
    server = null;
  }
  if (sseResp) {
    sseResp.destroy();
    sseResp = null;
  }
  sseSessionID = null;
  sseEventCh = [];
  callID = 1;
}

// Ensure cleanup on exit
process.on('exit', killServer);
process.on('SIGINT', () => { killServer(); process.exit(1); });
process.on('SIGTERM', () => { killServer(); process.exit(1); });

export interface RelayHelper {
  token: string;
  webURL: string;
  mcpURL: string;
  /** Submit a command via MCP request_command, returns request_id */
  submitCommand(command: string, args: string[], reason: string, opts?: { shell?: boolean }): Promise<string>;
  /** Approve a request via web API */
  approve(requestID: string): Promise<void>;
  /** Deny a request via web API */
  deny(requestID: string, reason?: string): Promise<void>;
  /** Add a whitelist rule */
  addWhitelist(command: string, args: string[]): Promise<void>;
  /** Activate turbo mode */
  activateTurbo(durationMinutes: number, cooldownSeconds: number): Promise<void>;
  /** Deactivate turbo mode */
  deactivateTurbo(): Promise<void>;
  /** Wait for a request to reach a terminal status */
  waitForComplete(requestID: string): Promise<any>;
}

// Shared test fixture
export const test = base.extend<{ relay: RelayHelper }>({
  relay: async ({}, use) => {
    if (!server) {
      server = await startRelay();
      const mcpURL = `http://127.0.0.1:${server.mcpPort}`;
      await connectSSE(mcpURL);
      // Initialize MCP session
      await mcpCall('initialize', {
        protocolVersion: '2024-11-05',
        capabilities: {},
        clientInfo: { name: 'playwright', version: '1.0' },
      });
      await mcpNotify('notifications/initialized');
    }

    const webURL = `http://127.0.0.1:${server.webPort}`;
    const mcpURL = `http://127.0.0.1:${server.mcpPort}`;

    const helper: RelayHelper = {
      token: TOKEN,
      webURL,
      mcpURL,

      async submitCommand(command, args, reason, opts) {
        const resp = await mcpCall('tools/call', {
          name: 'request_command',
          arguments: { command, args, reason, ...opts },
        });
        const text = resp.result.content[0].text;
        return JSON.parse(text).request_id;
      },

      async approve(requestID) {
        await httpReq('POST', `${webURL}/api/requests/${requestID}/approve`, null, {
          Authorization: `Bearer ${TOKEN}`,
        });
      },

      async deny(requestID, reason) {
        await httpReq('POST', `${webURL}/api/requests/${requestID}/deny`,
          { reason: reason || 'denied by test' },
          { Authorization: `Bearer ${TOKEN}` },
        );
      },

      async addWhitelist(command, args) {
        await httpReq('POST', `${webURL}/api/whitelist`,
          { command, args },
          { Authorization: `Bearer ${TOKEN}` },
        );
      },

      async activateTurbo(durationMinutes, cooldownSeconds) {
        await httpReq('POST', `${webURL}/api/turbocharge`,
          { duration_minutes: durationMinutes, cooldown_seconds: cooldownSeconds },
          { Authorization: `Bearer ${TOKEN}` },
        );
      },

      async deactivateTurbo() {
        await httpReq('DELETE', `${webURL}/api/turbocharge`, undefined,
          { Authorization: `Bearer ${TOKEN}` },
        );
      },

      async waitForComplete(requestID) {
        const deadline = Date.now() + 10_000;
        while (Date.now() < deadline) {
          const resp = await mcpCall('tools/call', {
            name: 'get_result',
            arguments: { request_id: requestID },
          });
          const r = JSON.parse(resp.result.content[0].text);
          if (r.status === 'complete' || r.status === 'error' || r.status === 'denied') {
            return r;
          }
          await new Promise(r => setTimeout(r, 200));
        }
        throw new Error('waitForComplete timeout');
      },
    };

    await use(helper);
  },
});

/** Navigate to dashboard and inject auth token */
export async function openDashboard(page: Page, relay: RelayHelper): Promise<void> {
  // Set localStorage token before navigating
  await page.addInitScript((token) => {
    localStorage.setItem('mhr_token', token);
  }, relay.token);
  await page.goto(relay.webURL);
  // Wait for the status bar to show "Connected"
  await page.waitForSelector('#connStatus:has-text("Connected")', { timeout: 5000 });
  // Give a moment for initial render
  await page.waitForTimeout(300);
}

export { expect };
