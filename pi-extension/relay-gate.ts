// pi-relay-gate
//
// Forwards every pi tool_call event to the human-relay /api/permission/check
// endpoint for an allow/deny/ask verdict. On 'ask', polls the relay until the
// human approves or denies in the dashboard. Fail-closed if the relay is
// unreachable or any error occurs — better to block a real call than to leak
// past a broken gate.
//
// Configuration via environment variables (read once at session start):
//   PI_RELAY_GATE_URL    — base URL of the relay web port, e.g.
//                          http://192.168.10.88:8090
//                          (NOT the MCP port; the permission endpoint lives
//                           on the web port behind bearer auth)
//   PI_RELAY_GATE_TOKEN  — bearer token (matches relay's MHR_AUTH_TOKEN)
//   PI_RELAY_GATE_CLIENT — cosmetic client tag (default "pi"); surfaced in
//                          the dashboard queue + audit log
//   PI_RELAY_GATE_TIMEOUT_MS    — per-request fetch timeout (default 5000)
//   PI_RELAY_GATE_ASK_TIMEOUT_S — max seconds to wait on an 'ask' verdict
//                                 (default 240; relay queue timeout is 300)

import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";

interface CheckResponse {
  verdict: "allow" | "deny" | "ask";
  rule_id?: string;
  reason?: string;
  request_id?: string;
}

interface StatusResponse {
  status: string;
  verdict?: "allow" | "deny" | "ask";
  reason?: string;
}

const URL = (process.env.PI_RELAY_GATE_URL || "").replace(/\/$/, "");
const TOKEN = process.env.PI_RELAY_GATE_TOKEN || "";
const CLIENT = process.env.PI_RELAY_GATE_CLIENT || "pi";
const FETCH_TIMEOUT_MS = Number(process.env.PI_RELAY_GATE_TIMEOUT_MS || 5000);
const ASK_TIMEOUT_S = Number(process.env.PI_RELAY_GATE_ASK_TIMEOUT_S || 240);

function getEventToolName(event: unknown): string | null {
  if (!event || typeof event !== "object") return null;
  const r = event as Record<string, unknown>;
  for (const k of ["toolName", "name", "tool"]) {
    const v = r[k];
    if (typeof v === "string" && v) return v;
  }
  return null;
}

function getEventInput(event: unknown): Record<string, unknown> {
  if (!event || typeof event !== "object") return {};
  const r = event as Record<string, unknown>;
  for (const k of ["input", "arguments", "args"]) {
    const v = r[k];
    if (v && typeof v === "object") return v as Record<string, unknown>;
  }
  return {};
}

async function fetchWithTimeout(url: string, opts: RequestInit, timeoutMs: number): Promise<Response> {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), timeoutMs);
  try {
    return await fetch(url, { ...opts, signal: ctl.signal });
  } finally {
    clearTimeout(timer);
  }
}

async function checkPermission(toolName: string, input: Record<string, unknown>): Promise<CheckResponse> {
  const res = await fetchWithTimeout(
    `${URL}/api/permission/check`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${TOKEN}`,
      },
      body: JSON.stringify({
        tool: toolName,
        input,
        reason: `pi tool_call: ${toolName}`,
        client: CLIENT,
      }),
    },
    FETCH_TIMEOUT_MS,
  );
  if (!res.ok) {
    throw new Error(`relay /permission/check returned ${res.status}`);
  }
  return (await res.json()) as CheckResponse;
}

async function pollAsk(requestId: string): Promise<StatusResponse> {
  const deadline = Date.now() + ASK_TIMEOUT_S * 1000;
  while (Date.now() < deadline) {
    try {
      const res = await fetchWithTimeout(
        `${URL}/api/permission/check/${requestId}`,
        { method: "GET", headers: { Authorization: `Bearer ${TOKEN}` } },
        FETCH_TIMEOUT_MS,
      );
      if (res.ok) {
        const body = (await res.json()) as StatusResponse;
        if (body.verdict === "allow" || body.verdict === "deny") {
          return body;
        }
      }
    } catch {
      // swallow transient errors, keep polling
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  return { status: "timeout", verdict: "deny", reason: "ask timeout — fail-closed" };
}

export default function register(pi: ExtensionAPI) {
  if (!URL || !TOKEN) {
    console.error(
      "[relay-gate] PI_RELAY_GATE_URL and PI_RELAY_GATE_TOKEN must be set; extension disabled.",
    );
    return;
  }
  console.error(`[relay-gate] enabled, posting to ${URL} as client=${CLIENT}`);

  pi.on("tool_call", async (event: any, _ctx: any) => {
    const toolName = getEventToolName(event);
    if (!toolName) {
      return { block: true, reason: "relay-gate: missing tool name on tool_call event" };
    }
    const input = getEventInput(event);

    let decision: CheckResponse;
    try {
      decision = await checkPermission(toolName, input);
    } catch (err: any) {
      return {
        block: true,
        reason: `relay-gate: permission check failed (${err?.message ?? err}); fail-closed`,
      };
    }

    if (decision.verdict === "allow") {
      return; // proceed
    }
    if (decision.verdict === "deny") {
      return {
        block: true,
        reason: `relay-gate denied ${toolName}: ${decision.reason ?? decision.rule_id ?? "policy"}`,
      };
    }
    if (decision.verdict === "ask") {
      if (!decision.request_id) {
        return { block: true, reason: "relay-gate: ask verdict missing request_id; fail-closed" };
      }
      const final = await pollAsk(decision.request_id);
      if (final.verdict === "allow") return;
      return {
        block: true,
        reason: `relay-gate: ${toolName} not approved (${final.reason ?? final.status})`,
      };
    }
    return { block: true, reason: `relay-gate: unknown verdict ${String(decision.verdict)}` };
  });
}
