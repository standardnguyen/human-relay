package ui

import (
	"strings"
	"testing"

	"github.com/standardnguyen/human-relay/tui/api"
)

// While the screen is too small to show a request, no key acts on it.
func TestNoDecisionKeyActsWhileTheScreenIsTooSmall(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1), mk("b", api.StatusPending, 2))
	m, _ = press(m, "enter") // detail of a, where decisions are allowed
	m, _ = Update(m, ResizeEvent{W: 30, H: 6})
	for _, key := range []string{"a", "g", "A", "d", "r", "w", "m", "t", "x", "P", "C", "y", "enter", "]", "j"} {
		m2, effs := press(m, key)
		if len(effs) != 0 || m2.Prompt != nil || m2.Sel != m.Sel || m2.Mode != m.Mode {
			t.Errorf("%s acted on a screen that is too small: effects %+v prompt %+v", key, effs, m2.Prompt)
		}
	}
	if !strings.Contains(Render(m, Theme{}), "too small") {
		t.Fatal("the screen says why nothing happens")
	}
	if _, effs := press(m, "q"); count[Quit](effs) != 1 {
		t.Fatalf("q still quits: %+v", effs)
	}
	if _, effs := press(m, "ctrl-c"); count[Quit](effs) != 1 {
		t.Fatalf("ctrl-c still quits: %+v", effs)
	}
}

func TestAnOpenPromptCannotBeAnsweredWhileTheScreenIsTooSmall(t *testing.T) {
	m := loaded(newModel(), mk("a", api.StatusPending, 1))
	m, _ = press(m, "enter", "A")
	if m.Prompt == nil {
		t.Fatal("no prompt")
	}
	m, _ = Update(m, ResizeEvent{W: 30, H: 6})
	m2, effs := press(m, "y")
	if len(effs) != 0 {
		t.Fatalf("a confirmation nobody can read was accepted: %+v", effs)
	}
	m3, _ := Update(m2, ResizeEvent{W: 100, H: 30})
	if m3.Prompt == nil {
		t.Fatal("the prompt is still open once the screen is big enough again")
	}
	if _, effs := press(m3, "y"); count[DoApprove](effs) != 1 {
		t.Fatalf("y works again at a usable size: %+v", effs)
	}
}

func TestTextTypedWhileTheScreenIsTooSmallIsNotCollected(t *testing.T) {
	m := newModel()
	m = m.purge()
	m, _ = Update(m, ResizeEvent{W: 20, H: 4})
	m, _ = press(m, "s", "e", "c")
	if m.Prompt != nil && len(m.Prompt.Secret) != 0 {
		t.Fatal("the hidden token prompt collected characters nobody could see")
	}
}
