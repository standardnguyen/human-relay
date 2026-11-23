package ui

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/standardnguyen/human-relay/tui/api"
)

var sgrRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

func screen(m Model) string { return Render(m, Theme{}) }

func detail(m Model, id string, width int) string {
	for _, r := range m.Requests {
		if r.ID == id {
			var b strings.Builder
			for _, l := range DetailLines(m, r, width) {
				b.WriteString(l.Plain() + "\n")
			}
			return b.String()
		}
	}
	panic("no request " + id)
}

func contains(t *testing.T, label, text string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(text, w) {
			t.Errorf("%s: missing %q in:\n%s", label, w, text)
		}
	}
}

func absent(t *testing.T, label, text string, nots ...string) {
	t.Helper()
	for _, w := range nots {
		if strings.Contains(text, w) {
			t.Errorf("%s: unexpected %q in:\n%s", label, w, text)
		}
	}
}

func utf16le(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestScreenShapeAtSeveralSizes(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusComplete, 2))
	for _, sz := range [][2]int{{40, 8}, {80, 24}, {120, 40}, {200, 12}} {
		m.Width, m.Height = sz[0], sz[1]
		out := screen(m)
		lines := strings.Split(out, "\n")
		if len(lines) != sz[1] {
			t.Errorf("%v: %d lines", sz, len(lines))
		}
		for i, l := range lines {
			if w := textWidth(l); w > sz[0] {
				t.Errorf("%v: line %d is %d cells wide: %q", sz, i, w, l)
			}
		}
	}
}

func TestScreenTooSmall(t *testing.T) {
	m := newModel()
	m.Width, m.Height = 30, 5
	out := screen(m)
	contains(t, "small", out, "too small")
	if len(strings.Split(out, "\n")) != 5 {
		t.Fatal("still exactly the terminal height")
	}
}

func TestHeaderShowsConnectionCountsTurboAndCooldown(t *testing.T) {
	m := newModel()
	out := screen(m)
	contains(t, "connecting", out, "http://127.0.0.1:8090", "Connecting")
	m, _ = Update(m, ConnEvent{Up: true})
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: &api.Listing{
		Requests:          []api.Request{mk("a", api.StatusPending, 1), mk("b", api.StatusComplete, 2)},
		TurboActive:       true,
		TurboRemaining:    90 * time.Second,
		CooldownRemaining: 12 * time.Second, CooldownDuration: 30 * time.Second}})
	out = screen(m)
	contains(t, "up", out, "Connected", "1 pending", "Turbo ON 1:30", "cooldown 12s")
	m, _ = Update(m, ConnEvent{Up: false})
	contains(t, "down", screen(m), "Disconnected", "retrying")
	m = loaded(m, mk("b", api.StatusComplete, 2), mk("c", api.StatusDenied, 3))
	contains(t, "total", screen(m), "2 total")
}

func TestTurboCountdownNeverShowsSixtySeconds(t *testing.T) {
	m := newModel()
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: &api.Listing{TurboActive: true, TurboRemaining: 119500 * time.Millisecond}})
	contains(t, "turbo", screen(m), "Turbo ON 2:00")
	absent(t, "turbo", screen(m), "1:60")
}

func TestHeaderMentionsTokenKind(t *testing.T) {
	m := newModel()
	m.TokenNote = "token accepted (approver, or a relay with no approver token)"
	contains(t, "token", screen(m), "token accepted")
}

func TestListRows(t *testing.T) {
	m := loaded(newModel(),
		mk("aaaa1111", api.StatusPending, 1, gated),
		mk("bbbb2222", api.StatusComplete, 2),
		mk("cccc3333", api.StatusDenied, 3))
	out := screen(m)
	contains(t, "rows", out, "aaaa1111", "bbbb2222", "cccc3333", "pending", "complete", "denied", "cmd-aaaa1111", "GATED")
	selected := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "aaaa1111") {
			selected = l
		}
	}
	if !strings.HasPrefix(strings.TrimLeft(selected, " "), ">") && !strings.Contains(selected, "> ") {
		t.Errorf("selected row not marked: %q", selected)
	}
}

func TestListEmptyState(t *testing.T) {
	m := loaded(newModel())
	contains(t, "empty", screen(m), "No requests")
}

func TestListLimitLines(t *testing.T) {
	var rs []api.Request
	for i := 0; i < 25; i++ {
		rs = append(rs, mk("r"+string(rune('a'+i)), api.StatusComplete, i))
	}
	m := loaded(newModel(), rs...)
	m.Height = 60
	contains(t, "limited", screen(m), "Showing 20 of 25", "s to show all")
	m, _ = press(m, "s")
	contains(t, "all", screen(m), "Showing all 25", "s to collapse")
}

func TestListShowsFilterBarWithWhitelistCount(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m.Rules = []api.Rule{{Command: "ls"}, {Command: "pwd"}}
	out := screen(m)
	contains(t, "filters", out, "All", "pending", "running", "complete", "denied", "withdrawn", "error", "approved", "timeout", "Whitelist (2)")
}

func TestWhitelistViewEmptyAndRules(t *testing.T) {
	m := newModel()
	m, _ = press(m, "9")
	contains(t, "empty", screen(m), "No whitelisted commands.", "Whitelist")
	m, _ = Update(m, WhitelistLoaded{Seq: 1, Rules: []api.Rule{{Command: "ls", Args: []string{"-l", "/tmp"}}, {Command: "uptime"}}})
	out := screen(m)
	contains(t, "rules", out, "ls", "-l", "/tmp", "uptime", "x remove")
}

func TestFooterShowsStatusAndKeyHints(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	out := screen(m)
	contains(t, "hints", out, "a approve", "g gated", "d deny", "? help", "q quit")
	m, _ = press(m, "enter")
	contains(t, "detail hints", screen(m), "esc back")
	m, _ = press(m, "esc")
	m = loaded(m, mk("a", api.StatusComplete, 1))
	m, _ = press(m, "a")
	contains(t, "status", screen(m), "not pending")
}

func TestPromptRendersOverTheList(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "d")
	m = typeText(m, "no thanks")
	out := screen(m)
	contains(t, "deny prompt", out, "Deny reason", "no thanks", "Enter", "Esc")
	m, _ = press(newModel(), "P")
	m = typeText(m, "mhra_supersecret")
	out = screen(m)
	if strings.Contains(out, "supersecret") || strings.Contains(out, "mhra_") {
		t.Fatalf("the token must never be drawn:\n%s", out)
	}
	contains(t, "token prompt", out, "token", "****************")
}

func TestHelpScreenListsKeys(t *testing.T) {
	m, _ := press(newModel(), "?")
	out := screen(m)
	contains(t, "help", out, "approve", "deny", "whitelist", "turbo", "release", "clear", "token", "quit", "legacy")
}

// --- detail view ---

func TestDetailCommandShowsEveryArgOnItsOwnLine(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) {
		r.Command, r.Args = "rsync", []string{"-av", "two words", "", "--exclude=*.tmp"}
		r.WorkingDir, r.Client, r.Timeout = "/srv/data", "agent-7", 45
	}))
	out := detail(m, "a", 80)
	contains(t, "argv", out, "rsync", "arg 1", "-av", "arg 2", "two words", "arg 3", "arg 4", "--exclude=*.tmp",
		"cwd: /srv/data", "client: agent-7", "timeout: 45s", "pending")
	absent(t, "no display command", out, "display:")
}

func TestDetailShowsDisplayCommandAndRealArgv(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) {
		r.DisplayCommand = "write -> ct1:/etc/app.conf  [12B, mode 0644]"
		r.Command = "ssh"
		r.Args = []string{"root@10.0.0.9", "--", "sh -c 'cat > /etc/app.conf'"}
		r.StdinLen = 12
		r.StdinSHA256 = "abc123"
	}))
	out := detail(m, "a", 100)
	contains(t, "both", out, "display: write -> ct1:/etc/app.conf", "ssh", "root@10.0.0.9", "sh -c 'cat > /etc/app.conf'", "FILE 12B", "show full file (12B)", "stdin sha256: abc123")
}

func TestDetailShellBannerOnlyWhilePending(t *testing.T) {
	sh := func(status string) Model {
		return loaded(newModel(), mk("a", status, 1, func(r *api.Request) { r.Shell, r.Command, r.Args = true, "echo hi | tee /tmp/x", nil }))
	}
	pending := detail(sh(api.StatusPending), "a", 100)
	contains(t, "pending", pending, "SHELL", "Shell execution \u2014 command will be interpreted by sh -c. Review carefully.", "shell: yes")
	done := detail(sh(api.StatusComplete), "a", 100)
	contains(t, "complete", done, "SHELL")
	absent(t, "complete", done, "Review carefully")
}

func TestDetailNonASCIIBadgeBannerAndFindings(t *testing.T) {
	cyr := func(status string) Model {
		return loaded(newModel(), mk("a", status, 1, func(r *api.Request) {
			r.Command = "l\u0455"
			r.Args = []string{"p\u0430th"}
			r.Raw = []byte(`{"command":"l\u0455","args":["p\u0430th"]}`)
		}))
	}
	pending := detail(cyr(api.StatusPending), "a", 100)
	contains(t, "pending", pending, "NON-ASCII", "Non-ASCII characters detected \u2014 check for homoglyph substitution.",
		"command: U+0455 (Cyrillic)", "args: U+0430 (Cyrillic)")
	done := detail(cyr(api.StatusComplete), "a", 100)
	contains(t, "complete", done, "NON-ASCII", "command: U+0455")
	absent(t, "complete", done, "check for homoglyph")
	// The homoglyph itself is shown, flagged, in the command line.
	for _, l := range DetailLines(cyr(api.StatusPending), cyr(api.StatusPending).Requests[0], 100) {
		for _, sp := range l {
			if strings.Contains(sp.Text, "\u0455") && sp.Style&Reverse == 0 {
				t.Errorf("non-ASCII character shown without highlight: %+v", sp)
			}
		}
	}
}

func TestDetailLinesBadge(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Command, r.Args = "sh", []string{"1\n2\n3\n4\n5\n6"} }))
	contains(t, "6 lines", detail(m, "a", 100), "6 lines", "scroll to review")
	m = loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Command, r.Args = "sh", []string{"1\n2\n3\n4\n5"} }))
	absent(t, "5 lines", detail(m, "a", 100), "scroll to review")
}

func TestDetailWrapsAndNeverClipsLongCommands(t *testing.T) {
	long := strings.Repeat("0123456789", 40) // 400 characters
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Command, r.Args = "echo", []string{long} }))
	var rebuilt strings.Builder
	for _, l := range DetailLines(m, m.Requests[0], 50) {
		if l.Width() > 50 {
			t.Fatalf("line wider than the screen: %d", l.Width())
		}
		rebuilt.WriteString(l.Plain())
	}
	if !strings.Contains(strings.ReplaceAll(rebuilt.String(), " ", ""), long) {
		t.Fatal("part of the argument is missing from the wrapped output")
	}
}

func TestDetailHTTPRequest(t *testing.T) {
	m := loaded(newModel(), mk("h", api.StatusPending, 1, func(r *api.Request) {
		r.Type, r.Command, r.Args = api.TypeHTTP, "", nil
		r.HTTPMethod, r.HTTPURL = "POST", "https://api.example.test/v1/items"
		r.HTTPHeaders = map[string]string{
			"Authorization": "Bearer abcdefghijklmnopqrstuvwxyz",
			"Content-Type":  "application/json",
			"X-Api-Key":     "0123456789abcdefTAIL",
			"Cookie":        "sid=abcdefghijklmnop",
		}
		r.HTTPBody = `{"name":"x","n":[1,2]}`
		r.Timeout = 30
	}))
	out := detail(m, "h", 100)
	contains(t, "http", out, "POST", "https://api.example.test/v1/items", "Authorization: Bearer abcde...", "Content-Type: application/json",
		"X-Api-Key: 0123456789ab...", "Cookie: sid=abcdefgh...", "\"name\": \"x\"", "timeout: 30s")
	absent(t, "redaction", out, "abcdefghijklmnopqrstuvwxyz", "TAIL", "klmnop")
	absent(t, "no shell badge", out, "SHELL")
}

func TestDetailHTTPBodyTruncationAndFullToggle(t *testing.T) {
	body := `{"k":"` + strings.Repeat("v", 3000) + `"}`
	m := loaded(newModel(), mk("h", api.StatusPending, 1, func(r *api.Request) {
		r.Type, r.Command, r.Args = api.TypeHTTP, "", nil
		r.HTTPMethod, r.HTTPURL, r.HTTPBody = "POST", "https://x.test/", body
	}))
	out := detail(m, "h", 100)
	contains(t, "truncated", out, "...(truncated)", "f show full body")
	if strings.Contains(strings.ReplaceAll(out, "\n", ""), strings.Repeat("v", 2500)) {
		t.Fatal("preview should be cut at 1024")
	}
	m, _ = press(m, "enter", "f")
	out = detail(m, "h", 100)
	if !strings.Contains(strings.ReplaceAll(strings.ReplaceAll(out, "\n", ""), " ", ""), strings.Repeat("v", 3000)) {
		t.Fatal("full body not shown after f")
	}
	absent(t, "full", out, "...(truncated)")
	contains(t, "toggle", out, "f hide full body")
}

func TestDetailHTTPFormFields(t *testing.T) {
	m := loaded(newModel(), mk("h", api.StatusPending, 1, func(r *api.Request) {
		r.Type, r.Command, r.Args = api.TypeHTTP, "", nil
		r.HTTPMethod, r.HTTPURL = "POST", "https://x.test/upload"
		r.HTTPFormFile = &api.FormFile{Field: "file", Filename: "report.xlsx", FetchCmd: []string{"ssh", "host", "cat", "/shared/report.xlsx"}, Source: "CTID 200:/shared/report.xlsx"}
		r.HTTPFormFields = map[string]string{"name": "report", "kind": "xlsx"}
		r.DisplayCommand = "POST https://x.test/upload [multipart file from CTID 200:/shared/report.xlsx]"
	}))
	out := detail(m, "h", 120)
	contains(t, "form", out, "form file: field=file filename=report.xlsx", "source: CTID 200:/shared/report.xlsx", "fetch cmd",
		"ssh", "/shared/report.xlsx", "form field: kind=xlsx", "form field: name=report", "display: POST https://x.test/upload")
}

func TestDetailScriptAndRegistryFields(t *testing.T) {
	m := loaded(newModel(),
		mk("s", api.StatusPending, 1, func(r *api.Request) {
			r.Type, r.Command, r.Args = api.TypeScript, "", nil
			r.ScriptName, r.ScriptArgs = "backup", []string{"--full", "/data"}
			r.DisplayCommand = "run_script backup --full /data"
		}),
		mk("r", api.StatusPending, 2, func(r *api.Request) {
			r.Type, r.Command, r.Args = api.TypeRegistryOp, "", nil
			r.RegistryOp = "register_container"
			r.RegistryArgs = map[string]string{"ctid": "4242", "ssh_user": "root", "host": "ct.internal.test"}
			r.DisplayCommand = "register a container"
		}))
	contains(t, "script", detail(m, "s", 100), "script: backup", "script arg 1: --full", "script arg 2: /data", "display: run_script backup --full /data")
	reg := detail(m, "r", 100)
	contains(t, "registry", reg, "registry op: register_container", "registry arg: ctid=4242", "registry arg: host=ct.internal.test", "registry arg: ssh_user=root")
	absent(t, "registry has no whitelist hint", reg, "A approve+whitelist")
}

func TestDetailReasonTruncationAndToggle(t *testing.T) {
	long := strings.Repeat("word ", 80) // 400 characters
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Reason = long }))
	out := detail(m, "a", 100)
	contains(t, "short", out, "...", "e show more")
	if strings.Count(strings.ReplaceAll(out, "\n", ""), "word") > 45 {
		t.Fatal("reason not truncated to 200 characters")
	}
	m, _ = press(m, "enter", "e")
	out = detail(m, "a", 100)
	contains(t, "long", out, "e show less")
	if strings.Count(strings.ReplaceAll(out, "\n", ""), "word") != 80 {
		t.Fatal("full reason not shown")
	}
}

func TestDetailBase64AndPowerShellBlocks(t *testing.T) {
	payload := "echo this is a hidden payload"
	ps := utf16le("Get-ChildItem C:\\secret")
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) {
		r.Command = "sh"
		r.Args = []string{"-c", "echo " + base64.StdEncoding.EncodeToString([]byte(payload)) + " | base64 -d | sh; powershell -NoProfile -enc " + ps}
		r.Shell = false
	}))
	out := detail(m, "a", 120)
	contains(t, "decoded", out, "Decoded base64", payload,
		"Decoded command (PowerShell -EncodedCommand):", "Get-ChildItem C:\\secret", "raw base64", "p show raw base64")
	absent(t, "raw hidden by default", out, ps+"\n")
	m, _ = press(m, "enter", "p")
	contains(t, "raw shown", detail(m, "a", 400), ps)
	m, _ = press(m, "b")
	out = detail(m, "a", 120)
	absent(t, "collapsed", out, payload)
	contains(t, "collapsed label", out, "Decoded base64")
}

func TestDetailPowerShellUndecodable(t *testing.T) {
	junk := base64.StdEncoding.EncodeToString([]byte{1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 6, 0, 7, 0, 8, 0, 9, 0})
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Command, r.Args = "pwsh", []string{"-e", junk} }))
	contains(t, "undecodable", detail(m, "a", 120), "PowerShell -EncodedCommand [could not decode]:", junk)
}

func TestDetailBase64NotShownForHTTP(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("a payload that is long enough"))
	m := loaded(newModel(), mk("h", api.StatusPending, 1, func(r *api.Request) {
		r.Type, r.Command, r.Args = api.TypeHTTP, "", nil
		r.HTTPMethod, r.HTTPURL, r.HTTPBody = "POST", "https://x.test/", payload
	}))
	absent(t, "http", detail(m, "h", 120), "Decoded base64")
}

func TestDetailFullContentStates(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, stdin(2048)))
	out := detail(m, "a", 100)
	contains(t, "collapsed", out, "FILE 2.0KB", "show full file (2.0KB)", "f ")
	m, _ = press(m, "enter", "f")
	contains(t, "loading", detail(m, "a", 100), "loading")
	m, _ = Update(m, ContentLoaded{ID: "a", Err: &api.Error{Status: 500, Body: "oops"}})
	contains(t, "failed", detail(m, "a", 100), "[failed to load content:", "500", "f to retry")
	m, _ = Update(m, ContentLoaded{ID: "a", Content: &api.Content{Body: []byte("first line\nsecond \x1b[31mred\x1b[0m\n"), SHA256: "ff"}})
	out = detail(m, "a", 100)
	contains(t, "loaded", out, "hide full file (2048 bytes, 2 lines)", "first line", `second \x1b[31mred\x1b[0m`)
	absent(t, "no raw escape", out, "\x1b")
}

func TestDetailFullContentShowsNonASCIIAndHashMismatch(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, stdin(5), func(r *api.Request) { r.StdinSHA256 = "aaaa" }))
	m, _ = press(m, "enter", "f")
	m, _ = Update(m, ContentLoaded{ID: "a", Content: &api.Content{Body: []byte("p\u0430ss\n"), SHA256: "bbbb"}})
	out := detail(m, "a", 100)
	contains(t, "content", out, "content differs from the listed stdin sha256", "non-ASCII in content: U+0430 (Cyrillic)")
}

func TestDetailResults(t *testing.T) {
	cases := []struct {
		name  string
		mod   func(*api.Request)
		wants []string
		nots  []string
	}{
		{"stdout and stderr", func(r *api.Request) {
			r.Result = &api.Result{ExitCode: 3, Stdout: "out line", Stderr: "err line"}
		}, []string{"stdout (exit 3)", "out line", "stderr", "err line"}, nil},
		{"no output", func(r *api.Request) { r.Result = &api.Result{ExitCode: 0} }, []string{"exit code: 0 (no output)"}, []string{"stdout"}},
		{"http result", func(r *api.Request) {
			r.Type, r.Command, r.Args = api.TypeHTTP, "", nil
			r.HTTPMethod, r.HTTPURL = "GET", "https://x.test/"
			r.Result = &api.Result{StatusCode: 404, Stdout: `{"error":"nope"}`, Stderr: "boom", ResponseHeaders: map[string]string{"X-Req": "1"}}
		}, []string{"response 404", "\"error\": \"nope\"", "error", "boom", "response headers", "H show"}, []string{"X-Req: 1"}},
		{"denied", func(r *api.Request) { r.Status, r.DenyReason = api.StatusDenied, "not today" }, []string{"Denied: not today"}, nil},
		{"withdrawn", func(r *api.Request) { r.Status, r.WithdrawReason = api.StatusWithdrawn, "changed my mind" }, []string{"Withdrawn by agent: changed my mind", "m mark read"}, nil},
		{"gated", func(r *api.Request) {
			r.Status, r.OutputGated = api.StatusComplete, true
			r.Result = &api.Result{Stdout: "[output gated]"}
		}, []string{"GATED", "r release"}, nil},
	}
	for _, c := range cases {
		m := loaded(newModel(), mk("a", api.StatusComplete, 1, c.mod))
		m, _ = press(m, "enter")
		var out string
		for _, l := range DetailLines(m, m.Requests[0], 100) {
			out += l.Plain() + "\n"
		}
		out += screen(m)
		contains(t, c.name, out, c.wants...)
		absent(t, c.name, out, c.nots...)
	}
}

func TestDetailHTTPResponseHeadersToggle(t *testing.T) {
	m := loaded(newModel(), mk("h", api.StatusComplete, 1, func(r *api.Request) {
		r.Type, r.Command, r.Args = api.TypeHTTP, "", nil
		r.HTTPMethod, r.HTTPURL = "GET", "https://x.test/"
		r.Result = &api.Result{StatusCode: 200, ResponseHeaders: map[string]string{"X-Req": "abc", "A-Header": "z"}}
	}))
	m, _ = press(m, "enter", "H")
	out := detail(m, "h", 100)
	contains(t, "headers", out, "A-Header: z", "X-Req: abc")
}

func TestDetailCooldownAndWhitelistedMarkers(t *testing.T) {
	m := newModel()
	seq++
	m, _ = Update(m, ListLoaded{Seq: seq, Listing: &api.Listing{
		Requests:          []api.Request{mk("a", api.StatusPending, 1), mk("b", api.StatusComplete, 2)},
		CooldownRemaining: 8*time.Second + 200*time.Millisecond, CooldownDuration: 30 * time.Second}})
	m.Rules = []api.Rule{{Command: "cmd-b", Args: []string{"a1"}}}
	contains(t, "cooldown", detail(m, "a", 100), "9s cooldown")
	out := detail(m, "b", 100)
	contains(t, "whitelisted", out, "Whitelisted")
	absent(t, "no cooldown on finished", out, "cooldown")
}

func TestDetailTimesIncludeDate(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusComplete, 1, func(r *api.Request) {
		d := t0.Add(2 * time.Minute)
		r.DecidedAt = &d
	}))
	out := detail(m, "a", 100)
	contains(t, "time", out, "2026-11-21", "decided")
}

func TestDetailScrollWindow(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusComplete, 1, func(r *api.Request) {
		var sb strings.Builder
		for i := 0; i < 100; i++ {
			sb.WriteString("output line number " + string(rune('A'+i%26)) + "\n")
		}
		r.Result = &api.Result{Stdout: sb.String()}
	}))
	m.Height = 20
	m, _ = press(m, "enter")
	top := screen(m)
	contains(t, "top", top, "a ", "stdout (exit 0)")
	m, _ = press(m, "pgdn", "pgdn", "pgdn", "pgdn", "pgdn", "pgdn", "pgdn", "pgdn")
	bottom := screen(m)
	absent(t, "scrolled", bottom, "stdout (exit 0)")
	contains(t, "bottom", bottom, "output line number")
	// The scroll position cannot run past the end.
	m, _ = press(m, "end", "pgdn", "pgdn")
	lines := DetailLines(m, m.Requests[0], m.Width)
	if m.DetailTop > len(lines) {
		t.Fatalf("scroll %d beyond %d lines", m.DetailTop, len(lines))
	}
}

// --- terminal safety on the final bytes ---

func hostileRequest() api.Request {
	esc := "\x1b[2J\x1b]52;c;aGVsbG8=\x07\x1b]8;;http://evil.test/\x1b\\\u009b31m\u202egnp\u200b"
	return api.Request{
		ID: "h0stile" + "\x1b[2J", Type: "", Status: "pending", Command: "ls" + esc,
		Args: []string{esc, "p\u0430ss"}, Reason: "reason" + esc, Client: "cl" + esc, WorkingDir: "/t" + esc,
		DisplayCommand: "disp" + esc, DenyReason: "deny" + esc, WithdrawReason: "wd" + esc, StdinLen: 9,
		HTTPMethod: "G" + esc, HTTPURL: "http://x/" + esc, HTTPHeaders: map[string]string{"H" + esc: "v" + esc, "Authorization": "x" + esc},
		HTTPBody: "body" + esc, HTTPFormFile: &api.FormFile{Field: "f" + esc, Filename: "n" + esc, Source: "s" + esc, FetchCmd: []string{"c" + esc}},
		HTTPFormFields: map[string]string{"k" + esc: "v" + esc}, ScriptName: "sn" + esc, ScriptArgs: []string{"sa" + esc},
		RegistryOp: "ro" + esc, RegistryArgs: map[string]string{"k" + esc: "v" + esc}, StdinSHA256: "sha" + esc,
		CreatedAt: t0,
		Result:    &api.Result{Stdout: "out" + esc, Stderr: "err" + esc, ResponseHeaders: map[string]string{"R" + esc: "v" + esc}, StatusCode: 200},
	}
}

func assertCleanBytes(t *testing.T, label, out string, theme Theme) {
	t.Helper()
	clean := out
	if theme.Color || theme.Attrs {
		clean = sgrRE.ReplaceAllString(out, "")
	}
	for i := 0; i < len(clean); {
		r, n := utf8.DecodeRuneInString(clean[i:])
		if r == utf8.RuneError && n == 1 {
			t.Fatalf("%s: invalid UTF-8 byte at %d", label, i)
		}
		if (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("%s: raw control %U at %d in %q", label, r, i, clean[max0(i-30):min(len(clean), i+30)])
		}
		switch r {
		case 0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0x2060, 0xFEFF, 0x061C, 0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069, 0x2028, 0x2029:
			t.Fatalf("%s: bidi/invisible %U survived", label, r)
		}
		i += n
	}
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestHostileFieldsNeverReachTheTerminalRaw(t *testing.T) {
	themes := map[string]Theme{"plain": {}, "color": {Color: true, Attrs: true}, "attrs": {Attrs: true}}
	for _, ascii := range []bool{false, true} {
		for name, th := range themes {
			m := NewModel(Options{URL: "http://h/" + "\x1b[2J", ASCII: ascii})
			m.Now, m.Width, m.Height = t0, 90, 40
			m.TokenNote = "note\x1b[2J"
			h := hostileRequest()
			h2 := h
			h2.ID = "second"
			h2.Status = "complete"
			seq++
			m, _ = Update(m, ListLoaded{Seq: seq, Listing: &api.Listing{Requests: []api.Request{h, h2}}})
			m.Rules = []api.Rule{{Command: "c" + "\x1b[2J", Args: []string{"\u202e"}}}
			m.Status = StatusLine{Text: "boom \x1b[2J", Err: true, At: t0}
			label := name
			if ascii {
				label += "/ascii"
			}
			assertCleanBytes(t, label+"/list", Render(m, th), th)
			m, _ = press(m, "enter", "f", "H", "p")
			m, _ = Update(m, ContentLoaded{ID: m.Sel, Content: &api.Content{Body: []byte("file\x1b[2J\x9b31m\xff\u202e")}})
			out := Render(m, th)
			assertCleanBytes(t, label+"/detail", out, th)
			for off := 0; off < 400; off += 17 {
				m.DetailTop = off
				assertCleanBytes(t, label+"/detail-scrolled", Render(m, th), th)
			}
			m, _ = press(m, "esc", "?")
			assertCleanBytes(t, label+"/help", Render(m, th), th)
			m, _ = press(m, "esc", "9")
			assertCleanBytes(t, label+"/whitelist", Render(m, th), th)
			m, _ = press(m, "x")
			assertCleanBytes(t, label+"/confirm", Render(m, th), th)
			if ascii {
				if strings.Contains(out, "\x1b") {
					t.Fatalf("%s: ascii mode must not emit any escape sequence", label)
				}
				for i := 0; i < len(out); i++ {
					if out[i] >= 0x80 {
						t.Fatalf("%s: ascii mode emitted byte %#x", label, out[i])
					}
				}
			}
			if name == "plain" && strings.Contains(out, "\x1b") {
				t.Fatalf("%s: no-colour theme emitted an escape", label)
			}
		}
	}
}

func TestHostileTextIsReadableAsEscapes(t *testing.T) {
	m := newModel()
	h := hostileRequest()
	m = loaded(m, h)
	out := detail(m, h.ID, 200)
	contains(t, "escapes visible", out, `\x1b[2J`, `\x1b]52;c;aGVsbG8=\x07`, "<U+202E>", "<U+200B>", "<U+009B>")
}

func TestColourModeHighlightsFlaggedRunes(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Command = "p\u0430y\x1b" }))
	out := Render(m, Theme{Color: true, Attrs: true})
	if !regexp.MustCompile("\x1b\\[[0-9;]*7[0-9;]*m").MatchString(out) {
		t.Fatalf("flagged text should be drawn in reverse video")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusComplete, 2))
	if screen(m) != screen(m) {
		t.Fatal("rendering must be a pure function of the model")
	}
}
