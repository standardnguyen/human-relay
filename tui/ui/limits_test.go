package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/standardnguyen/human-relay/tui/api"
)

// A pending row is cut to the screen width before it is sanitized, so a very
// large command costs the same to draw as a short one.
func TestListRowWorkIsBoundedByTheScreenWidth(t *testing.T) {
	huge := strings.Repeat("\x1b", 1<<20)
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Args = []string{huge} }))
	allocs := testing.AllocsPerRun(3, func() { Render(m, Theme{}) })
	if allocs > 3000 {
		t.Fatalf("%.0f allocations drawing one list row with a 1 MiB command", allocs)
	}
	out := Render(m, Theme{})
	if !strings.Contains(out, "\\x1b") || strings.Contains(out, "\x1b") {
		t.Fatalf("row must show escaped control bytes only:\n%q", out[:200])
	}
}

func TestListRowShowsACutMarkerWhenTheSummaryIsLong(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Args = []string{strings.Repeat("z", 5000)} }))
	m.Opts.ASCII = true
	if out := Render(m, Theme{}); !strings.Contains(out, "...") {
		t.Fatalf("a cut row ends in a marker:\n%s", out)
	}
}

// Drawing the same screen again costs next to nothing, however large the
// request is: whole-request scans are done once per request, not once per frame.
func TestRedrawingALargeRequestIsCheap(t *testing.T) {
	redraw := func(m Model) time.Duration {
		Render(m, Theme{}) // the first draw may scan the request
		start := time.Now()
		for i := 0; i < 3; i++ {
			Render(m, Theme{})
		}
		return time.Since(start) / 3
	}
	huge := loaded(newModel(), mk("a", api.StatusPending, 1, func(r *api.Request) { r.Args = []string{strings.Repeat("\x1b", 4<<20)} }))
	if d := redraw(huge); d > 100*time.Millisecond {
		t.Fatalf("list redraw with a 4 MiB command takes %v", d)
	}
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < 200000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":123456,"name":"abc"}`)
	}
	b.WriteString("]}")
	body := loaded(newModel(), httpReq(b.String()))
	body, _ = press(body, "enter")
	if d := redraw(body); d > 100*time.Millisecond {
		t.Fatalf("detail redraw with a %d byte JSON body takes %v", b.Len(), d)
	}
}
