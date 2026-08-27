package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

var chatScriptBlockRe = regexp.MustCompile(`(?s)<script>(.*)</script>`)

// TestChatGroupingLogic drives the real client-side mapRequest/rebuild JS —
// extracted live from chat.html, not a copy, so this can't silently drift
// from the shipped code — against realistic sample request shapes via Node,
// to verify group messages thread into one conversation instead of
// splitting by sender UUID (the pre-fix behavior), and that a raw
// http_request send (no script_name) stays excluded from the chat lens.
// Regression coverage for the 2026-08-27 group-chat fix: signal-poll.py
// resolves group_id/group_name via a live /v1/groups lookup, and chat.html
// keys a group message's conversation by that resolved id instead of the
// sender's UUID.
func TestChatGroupingLogic(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available on this machine")
	}

	html, err := os.ReadFile(filepath.Join("..", "web", "templates", "chat.html"))
	if err != nil {
		t.Fatalf("read chat.html: %v", err)
	}
	m := chatScriptBlockRe.FindSubmatch(html)
	if m == nil {
		t.Fatalf("could not find <script>...</script> block in chat.html")
	}
	appScript := string(m[1])

	tmp := t.TempDir()
	driverPath := filepath.Join(tmp, "drive.js")
	if err := os.WriteFile(driverPath, []byte(chatGroupingDriver(appScript)), 0o644); err != nil {
		t.Fatalf("write driver script: %v", err)
	}

	cmd := exec.Command("node", driverPath)
	out, err := cmd.CombinedOutput()
	t.Logf("node driver output:\n%s", out)
	if err != nil {
		t.Fatalf("chat grouping checks failed: %v", err)
	}
}

// chatGroupingDriver wraps the real app script with browser-API stubs (no
// DOM needed -- mapRequest/rebuild are pure logic), sample request data
// covering the cases that matter, and assertions. Exits non-zero on any
// failed check so the Go test can surface it via CombinedOutput's error.
func chatGroupingDriver(appScript string) string {
	return `
const GROUP_KEY = "group.bmo5bmQrOWl2Rm9wTlMyZmhxRG1JMWlSenp3TUVoazducGlxaEhuREhoTT0=";
const OPERATOR_UUID = "944c412b-6e81-45e8-b7e9-b5036a8a5e40";

// --- browser API stubs -- mapRequest/rebuild never touch these, but the
// script's top-level init code (load(); connectSSE(); setInterval(...)) does
// on load, so they need to exist without throwing. ---
globalThis.localStorage = { getItem: () => "fake-token", setItem: () => {} };
globalThis.prompt = () => "fake-token";
globalThis.fetch = () => Promise.resolve({ ok: false, status: 0 });
globalThis.EventSource = function () { this.onopen = null; this.onmessage = null; this.onerror = null; };
globalThis.document = { getElementById: () => ({}), createElement: () => ({}) };
globalThis.setInterval = () => {};
globalThis.setTimeout = () => {};

` + appScript + `

function check(label, cond) {
  if (!cond) { console.error("FAIL:", label); failures++; }
  else { console.log("ok:", label); }
}
let failures = 0;

const requestsIn = [
  // 1. Inbound group message from a real friend, resolved by the updated
  //    signal-poll.py (live /v1/groups member-match lookup).
  {
    id: "req-in-1", type: "script", script_name: "signal-read",
    status: "complete", output_gated: false, created_at: "2026-08-27T19:00:00Z",
    result: { stdout: JSON.stringify({
      ts: 1, from_uuid: "3dcfce35-72e4-4745-bcde-13902fa48bfc", from_name: "Mariana",
      from_number: null, message: "omg can our claudes really set up my dates",
      group_info: { groupId: "raw-undecoded-form" },
      group_id: GROUP_KEY, group_name: "Mexworld",
    }) },
  },
  // 2. Outbound reply into the same group via the signal-send SCRIPT path
  //    (not raw http_request -- this is what should render at all).
  {
    id: "req-out-1", type: "script", script_name: "signal-send",
    status: "complete", created_at: "2026-08-27T19:01:00Z",
    script_args: [GROUP_KEY, "ha, working on it -- logistics-only or the full thing?"],
  },
  // 3. The operator's OWN group post -- lands in the standard spool
  //    (sourceUuid == operator), but must still route to the group
  //    conversation, not her personal "Standard (you)" bubble.
  {
    id: "req-in-2", type: "script", script_name: "signal-read-standard",
    status: "complete", created_at: "2026-08-27T19:02:00Z",
    result: { stdout: JSON.stringify({
      ts: 2, from_uuid: OPERATOR_UUID, from_name: "Standard Nguyen",
      from_number: null, message: "bb respond if you can read this",
      group_info: { groupId: "raw-undecoded-form" },
      group_id: GROUP_KEY, group_name: "Mexworld",
    }) },
  },
  // 4. A genuine 1:1 DM from the operator (no group_info) -- must stay in
  //    the plain "standard" bucket, unaffected by the group logic.
  {
    id: "req-in-3", type: "script", script_name: "signal-read-standard",
    status: "complete", created_at: "2026-08-27T18:00:00Z",
    result: { stdout: JSON.stringify({
      ts: 3, from_uuid: OPERATOR_UUID, from_name: "Standard Nguyen",
      from_number: null, message: "unrelated 1:1 message", group_info: null,
    }) },
  },
  // 5. A genuine 1:1 DM from a third party (others lane, no group) -- must
  //    still key by their own UUID, unaffected by the group changes.
  {
    id: "req-in-4", type: "script", script_name: "signal-read",
    status: "complete", output_gated: false, created_at: "2026-08-27T17:00:00Z",
    result: { stdout: JSON.stringify({
      ts: 4, from_uuid: "1e957343-3870-46db-95d1-37e4778b1976", from_name: "astra",
      from_number: null, message: "meow this is astra", group_info: null,
    }) },
  },
  // 6. A raw http_request send (no script_name at all) -- must stay
  //    excluded from /chat, same as before this fix.
  {
    id: "req-raw-1", type: "http", status: "complete", created_at: "2026-08-27T18:17:00Z",
    http_method: "POST", http_url: "http://signal-api:8080/v2/send",
  },
];

requests = requestsIn;
rebuild();

const keys = Object.keys(convos);
console.log("convo keys:", keys);

check("group conversation exists under the group.<base64> key", !!convos[GROUP_KEY]);
check("group conversation named Mexworld, not a hash", convos[GROUP_KEY] && convos[GROUP_KEY].name === "Mexworld");
check("group conversation has all 3 group messages threaded together",
  convos[GROUP_KEY] && convos[GROUP_KEY].messages.length === 3);
check("standard (1:1) conversation exists, separate from the group, with only the true 1:1 message",
  !!convos["standard"] && convos["standard"].messages.length === 1
    && convos["standard"].messages[0].text === "unrelated 1:1 message");
check("third-party 1:1 conversation exists under their own UUID, unaffected by group logic",
  !!convos["1e957343-3870-46db-95d1-37e4778b1976"]);
check("exactly 3 conversations total -- raw http_request send stayed excluded",
  keys.length === 3);

process.exit(failures ? 1 : 0);
`
}
