package web

// Tests for the approval dashboard's "show full file" toggle
// (buildContentToggle / countLines / applyFullContentState / toggleFullContent
// in templates/index.html).
//
// Three approver-facing bugs are pinned here:
//
//  1. The expanded label reported text.length -- UTF-16 code units, not bytes --
//     so a 4102-byte UTF-8 file rendered "hide full file (2502 bytes, ...)"
//     directly beneath a FILE 4.0KB badge computed from the server's byte
//     count. Two labels for one file disagreeing is exactly the kind of thing
//     that makes a reviewer distrust the pane. The byte figure now comes from
//     the card's stdin_len, the same number the badge uses.
//  2. text.split('\n').length counts the empty element after a trailing
//     newline, so every normal text file was reported one line too long.
//  3. The fetched bytes and the expanded state lived in the DOM, and render()
//     replaces the whole card list with innerHTML on every SSE message. An
//     unrelated write_file arriving mid-read silently collapsed the pane and
//     dropped the content. The state now lives in a Map that render()
//     re-applies.
//
// The code is JavaScript, so -- like nonascii_check_test.go -- the tests
// extract it from the embedded template and execute it under node against a
// stub DOM. A Go reimplementation would test the copy, not the code that ships.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Boundaries of the two regions of index.html this test executes. Slices
// rather than the whole <script>, so the code runs outside a browser.
const (
	contentToggleJSStart = "const fullContentState = new Map();"
	contentToggleJSEnd   = "function toggleTruncate(id) {"
	formatSizeJSStart    = "function formatSize(bytes) {"
	formatSizeJSEnd      = "function buildCommandDisplay(r) {"
)

func extractRegion(t *testing.T, src, start, end, what string) string {
	t.Helper()
	i := strings.Index(src, start)
	j := strings.Index(src, end)
	if i < 0 || j < 0 || j <= i {
		t.Fatalf("could not locate %s in templates/index.html "+
			"(start marker found=%v, end marker found=%v) -- if the code moved, "+
			"update the markers in content_toggle_test.go", what, i >= 0, j >= 0)
	}
	return src[i:j]
}

// contentToggleHarness is the stub environment the extracted code runs in: a
// fake document keyed by element id, a fetch that serves one fixture and counts
// calls, and a renderCards() that reproduces what render() does to the DOM --
// throw every node away and rebuild from buildContentToggle's HTML.
const contentToggleHarness = `
// ---- stub environment -------------------------------------------------
const DOM = new Map();
global.document = { getElementById: (id) => DOM.get(id) || null };
const headers = {};
function stripBidi(s) { return s; }

const lineAt = (i) => 'line ' + String(i).padStart(3, '0') +
  ' — em-dash — padded so bytes and UTF-16 units diverge';
const FIXTURE = Array.from({ length: 120 }, (_, k) => lineAt(k + 1)).join('\n') + '\n';
const BYTES = Buffer.byteLength(FIXTURE, 'utf8');

let fetchCount = 0, lastURL = null;
global.fetch = async (url) => { fetchCount++; lastURL = url; return { ok: true, text: async () => FIXTURE }; };

let requests = [];

// Stand-in for render(): innerHTML replaces the card list wholesale, so every
// node and every dataset on it is destroyed. Attributes are parsed back out of
// buildContentToggle's own HTML, so a toggle that stops emitting data-bytes or
// data-label shows up here as a broken label rather than passing quietly.
function renderCards() {
  DOM.clear();
  for (const r of requests) {
    const html = buildContentToggle(r);
    const m = /id="cf-([^"]+)-link"([^>]*)>/.exec(html);
    if (!m) throw new Error('buildContentToggle emitted no link element: ' + html);
    const label = /data-label="([^"]*)"/.exec(m[2]);
    const bytes = /data-bytes="([^"]*)"/.exec(m[2]);
    const link = { textContent: '', style: { display: '' }, dataset: {} };
    if (label) link.dataset.label = label[1];
    if (bytes) link.dataset.bytes = bytes[1];
    // The link's rendered text is whatever buildContentToggle put between the tags.
    const inner = />([^<]*)<\/span>/.exec(html);
    link.textContent = inner ? inner[1] : '';
    DOM.set('cf-' + r.id + '-link', link);
    const hidden = /id="cf-[^"]+-body"[^>]*style="display:none"/.test(html);
    DOM.set('cf-' + r.id + '-body', {
      textContent: '', style: { display: hidden ? 'none' : '' }, dataset: {},
    });
  }
  if (typeof applyFullContentState === 'function') applyFullContentState();
}

// ---- the run ----------------------------------------------------------
const link = () => DOM.get('cf-aaa-link');
const body = () => DOM.get('cf-aaa-body');
const out = { bytes: BYTES, chars: FIXTURE.length, size_label: formatSize(BYTES) };

(async () => {
  const cardA = { id: 'aaa', stdin_len: BYTES };
  const cardB = { id: 'bbb', stdin_len: 42 };

  requests = [cardA];
  renderCards();
  out.collapsed_label = link().textContent;
  out.body_hidden_initially = body().style.display === 'none';

  await toggleFullContent('aaa');
  out.expanded_label = link().textContent;
  out.expanded_display = body().style.display;
  out.expanded_text_len = body().textContent.length;
  out.expanded_tail_ok = body().textContent.endsWith(lineAt(120) + '\n');
  out.fetch_count_after_expand = fetchCount;
  out.fetch_url = lastURL;

  // An unrelated write_file arrives: SSE -> fetchRequests() -> render().
  requests = [cardA, cardB];
  renderCards();
  out.label_after_rerender = link().textContent;
  out.display_after_rerender = body().style.display;
  out.text_len_after_rerender = body().textContent.length;
  out.tail_after_rerender_ok = body().textContent.endsWith(lineAt(120) + '\n');
  out.fetch_count_after_rerender = fetchCount;

  await toggleFullContent('aaa');
  out.display_after_collapse = body().style.display;
  out.label_after_collapse = link().textContent;

  renderCards();
  out.display_after_collapse_rerender = body().style.display;
  out.fetch_count_final = fetchCount;

  // The request leaves the list: its cached bytes must not be held forever.
  requests = [cardB];
  renderCards();
  out.state_size_after_drop = fullContentState.size;

  process.stdout.write(JSON.stringify(out));
})().catch((e) => { process.stderr.write(String(e && e.stack || e)); process.exit(1); });
`

type toggleResult struct {
	Bytes                     int    `json:"bytes"`
	Chars                     int    `json:"chars"`
	SizeLabel                 string `json:"size_label"`
	CollapsedLabel            string `json:"collapsed_label"`
	BodyHiddenInitially       bool   `json:"body_hidden_initially"`
	ExpandedLabel             string `json:"expanded_label"`
	ExpandedDisplay           string `json:"expanded_display"`
	ExpandedTextLen           int    `json:"expanded_text_len"`
	ExpandedTailOK            bool   `json:"expanded_tail_ok"`
	FetchCountAfterExpand     int    `json:"fetch_count_after_expand"`
	FetchURL                  string `json:"fetch_url"`
	LabelAfterRerender        string `json:"label_after_rerender"`
	DisplayAfterRerender      string `json:"display_after_rerender"`
	TextLenAfterRerender      int    `json:"text_len_after_rerender"`
	TailAfterRerenderOK       bool   `json:"tail_after_rerender_ok"`
	FetchCountAfterRerender   int    `json:"fetch_count_after_rerender"`
	DisplayAfterCollapse      string `json:"display_after_collapse"`
	LabelAfterCollapse        string `json:"label_after_collapse"`
	DisplayAfterCollapseRerun string `json:"display_after_collapse_rerender"`
	FetchCountFinal           int    `json:"fetch_count_final"`
	StateSizeAfterDrop        int    `json:"state_size_after_drop"`
}

func runContentToggleJS(t *testing.T) toggleResult {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH -- cannot execute the dashboard JS; install node to run this test")
	}
	raw, err := templateFS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read embedded templates/index.html: %v", err)
	}
	src := string(raw)
	script := extractRegion(t, src, formatSizeJSStart, formatSizeJSEnd, "formatSize") +
		extractRegion(t, src, contentToggleJSStart, contentToggleJSEnd, "the full-content toggle") +
		contentToggleHarness

	path := filepath.Join(t.TempDir(), "toggle.js")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write js: %v", err)
	}
	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("node failed: %v\n%s", err, out)
	}
	var res toggleResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("parse node output %q: %v", out, err)
	}
	return res
}

// TestContentToggleLabelCountsBytesAndLines pins bugs 1 and 2: the byte figure
// must be the server's stdin_len (what the FILE badge shows), and a trailing
// newline must not invent a 121st line.
func TestContentToggleLabelCountsBytesAndLines(t *testing.T) {
	r := runContentToggleJS(t)

	// Control: if the fixture's byte length ever equals its UTF-16 length, the
	// bytes-vs-code-units assertion below reads the same either way and proves
	// nothing. Fail loudly rather than pass for the wrong reason.
	if r.Bytes == r.Chars {
		t.Fatalf("fixture is %d bytes and %d UTF-16 units -- identical, so this test "+
			"cannot distinguish a byte count from a string length; the multi-byte "+
			"padding in the harness fixture is gone", r.Bytes, r.Chars)
	}

	if want := "show full file (" + r.SizeLabel + ")"; r.CollapsedLabel != want {
		t.Errorf("collapsed label = %q, want %q", r.CollapsedLabel, want)
	}
	if !r.BodyHiddenInitially {
		t.Errorf("content body should start hidden")
	}

	want := "hide full file (" + strconv.Itoa(r.Bytes) + " bytes, 120 lines)"
	if r.ExpandedLabel != want {
		t.Errorf("expanded label = %q, want %q\n"+
			"  the fixture is %d bytes / %d UTF-16 code units / 120 lines with a trailing newline.\n"+
			"  %q means the label counted UTF-16 units instead of the card's stdin_len;\n"+
			"  \"121 lines\" means split('\\n') counted the empty element after the final newline.",
			r.ExpandedLabel, want, r.Bytes, r.Chars,
			"hide full file ("+strconv.Itoa(r.Chars)+" bytes, 121 lines)")
	}
}

// TestContentToggleSurvivesRerender pins bug 3: render() rebuilds the card list
// on every SSE message, and an approver mid-read must not be collapsed -- nor
// silently shown an empty pane -- because somebody else queued a request.
func TestContentToggleSurvivesRerender(t *testing.T) {
	r := runContentToggleJS(t)

	if r.ExpandedDisplay != "" || r.ExpandedTextLen != r.Chars || !r.ExpandedTailOK {
		t.Fatalf("after expanding: display=%q text=%d chars (want %d) tail_present=%v",
			r.ExpandedDisplay, r.ExpandedTextLen, r.Chars, r.ExpandedTailOK)
	}
	if r.FetchURL != "/api/requests/aaa/content" {
		t.Errorf("fetched %q, want /api/requests/aaa/content", r.FetchURL)
	}

	if r.DisplayAfterRerender != "" || r.TextLenAfterRerender != r.Chars || !r.TailAfterRerenderOK {
		t.Errorf("an unrelated request re-rendered the list and the open pane did not survive: "+
			"display=%q (want \"\" = visible) text=%d chars (want %d) last_line_present=%v; "+
			"label=%q. render() replaces the card list with innerHTML, so the <pre> and its "+
			"cache are destroyed -- the fetched bytes and the expanded flag have to live "+
			"outside the DOM and be re-applied after each render",
			r.DisplayAfterRerender, r.TextLenAfterRerender, r.Chars,
			r.TailAfterRerenderOK, r.LabelAfterRerender)
	}
	if r.LabelAfterRerender != r.ExpandedLabel {
		t.Errorf("label after re-render = %q, want the expanded label %q",
			r.LabelAfterRerender, r.ExpandedLabel)
	}

	if r.FetchCountAfterExpand != 1 || r.FetchCountAfterRerender != 1 || r.FetchCountFinal != 1 {
		t.Errorf("content fetched %d/%d/%d times (expand/after re-render/final), want 1 each -- "+
			"the content must be fetched once and cached, not re-pulled on every SSE message",
			r.FetchCountAfterExpand, r.FetchCountAfterRerender, r.FetchCountFinal)
	}

	if r.DisplayAfterCollapse != "none" || r.LabelAfterCollapse != r.CollapsedLabel {
		t.Errorf("after collapsing: display=%q label=%q, want \"none\" and %q",
			r.DisplayAfterCollapse, r.LabelAfterCollapse, r.CollapsedLabel)
	}
	if r.DisplayAfterCollapseRerun != "none" {
		t.Errorf("a re-render re-opened a pane the approver had collapsed (display=%q)",
			r.DisplayAfterCollapseRerun)
	}

	if r.StateSizeAfterDrop != 0 {
		t.Errorf("cached content for %d request(s) survived their removal from the request "+
			"list -- the Map is unbounded and grows for the life of the tab", r.StateSizeAfterDrop)
	}
}

// TestRenderReappliesContentState pins the call site: the Map only helps if
// render() actually re-applies it after replacing the card list. The JS test
// above drives applyFullContentState directly, so it cannot see the call go
// missing from render().
func TestRenderReappliesContentState(t *testing.T) {
	raw, err := templateFS.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read embedded templates/index.html: %v", err)
	}
	const marker = "el.innerHTML = html;"
	i := strings.Index(string(raw), marker)
	if i < 0 {
		t.Fatalf("could not find %q in templates/index.html -- render() changed shape, "+
			"update this test", marker)
	}
	tail := string(raw)[i+len(marker):]
	if end := strings.Index(tail, "\n}"); end >= 0 {
		tail = tail[:end]
	}
	if !strings.Contains(tail, "applyFullContentState()") {
		t.Errorf("render() writes the card list with %q and does not call "+
			"applyFullContentState() before returning, so every fetched file body and "+
			"every expanded pane is discarded on the next SSE message. Tail of render(): %q",
			marker, tail)
	}
}
