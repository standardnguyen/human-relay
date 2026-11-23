package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/standardnguyen/human-relay/tui/api"
)

// frameWriter turns frames into the minimal terminal output: only lines that
// changed since the previous frame are rewritten.
type frameWriter struct{ prev []string }

func (f *frameWriter) invalidate() { f.prev = nil }

func (f *frameWriter) diff(frame string, rows int) string {
	lines := strings.Split(frame, "\n")
	var b strings.Builder
	for i := 0; i < rows; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		if i < len(f.prev) && f.prev[i] == line {
			continue
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[0m\x1b[K%s\x1b[0m", i+1, line)
	}
	f.prev = make([]string, rows)
	for i := 0; i < rows && i < len(lines); i++ {
		f.prev[i] = lines[i]
	}
	return b.String()
}

// wipeBytes zeroes b. Typed input can be a token, so it is not left behind.
func wipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// pump reads r until it ends and hands each chunk to send as a copy it owns;
// the read buffer is zeroed after every read. It returns when r fails or send
// reports false.
func pump(r io.Reader, buf []byte, send func([]byte) bool) {
	defer wipeBytes(buf)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			wipeBytes(buf[:n])
			if !send(chunk) {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// RunConfig is what the terminal loop needs.
type RunConfig struct {
	Client *api.Client
	Opts   Options
	Theme  Theme
	In     *os.File
	Out    io.Writer
}

const (
	enterScreen = "\x1b[?1049h\x1b[?25l\x1b[?2004h\x1b[2J"
	leaveScreen = "\x1b[0m\x1b[?2004l\x1b[?25h\x1b[?1049l"
)

// Run owns the terminal until the user quits: raw mode, the alternate screen,
// resize handling, and restoring everything on exit and on panic.
func Run(ctx context.Context, cfg RunConfig) (err error) {
	fd := int(cfg.In.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("input is not a terminal")
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	out := cfg.Out
	io.WriteString(out, enterScreen)
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		io.WriteString(out, leaveScreen)
		term.Restore(fd, old)
	}
	defer func() {
		r := recover()
		restore()
		cfg.Client.Wipe()
		if r != nil {
			panic(r)
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan Event, 512)
	post := func(e Event) {
		select {
		case events <- e:
		case <-ctx.Done():
		}
	}
	panics := make(chan any, 1)
	safely := func(f func()) {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					select {
					case panics <- r:
					default:
					}
				}
			}()
			f()
		}()
	}

	// input
	raw := make(chan []byte, 16)
	safely(func() {
		pump(cfg.In, make([]byte, 4096), func(b []byte) bool {
			select {
			case raw <- b:
				return true
			case <-ctx.Done():
				return false
			}
		})
		post(KeyEvent{Key: Key{Type: KeyCtrlC}})
	})

	// signals
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer signal.Stop(sig)

	// event stream and ticks
	safely(func() {
		cfg.Client.Watch(ctx, api.DefaultBackoff,
			func(up bool) { post(ConnEvent{Up: up}) },
			func(f api.Frame) { post(FrameEvent{Frame: f}) })
	})
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	m := NewModel(cfg.Opts)
	if w, h, e := term.GetSize(fd); e == nil {
		m.Width, m.Height = w, h
	}
	m.Now = time.Now()
	var effs []Effect
	m, effs = Init(m)

	var fw frameWriter
	var pending []byte
	var escTimer <-chan time.Time
	redraw := func() {
		io.WriteString(out, fw.diff(Render(m, cfg.Theme), m.Height))
	}
	run := func(effs []Effect) bool {
		for _, e := range effs {
			switch x := e.(type) {
			case Quit:
				return true
			case RingBell:
				io.WriteString(out, "\a")
			default:
				runEffect(ctx, cfg.Client, x, post, safely)
			}
		}
		return false
	}
	step := func(ev Event) bool {
		m.Now = time.Now()
		m, effs = Update(m, ev)
		return run(effs)
	}
	if run(effs) {
		return nil
	}
	redraw()

	for {
		select {
		case <-ctx.Done():
			return nil
		case r := <-panics:
			panic(r)
		case s := <-sig:
			if s == syscall.SIGWINCH {
				if w, h, e := term.GetSize(fd); e == nil {
					fw.invalidate()
					io.WriteString(out, "\x1b[2J")
					if step(ResizeEvent{W: w, H: h}) {
						return nil
					}
				}
			} else {
				return nil
			}
		case <-ticker.C:
			if step(TickEvent{Now: time.Now()}) {
				return nil
			}
		case b := <-raw:
			pending = append(pending, b...)
			wipeBytes(b)
			keys, rest := ParseKeys(pending, false)
			wipeBytes(pending[:len(pending)-len(rest)])
			pending = rest
			escTimer = nil
			if len(pending) > 0 {
				escTimer = time.After(50 * time.Millisecond)
			}
			for _, k := range keys {
				if k.Type == KeyCtrlL {
					fw.invalidate()
					io.WriteString(out, "\x1b[2J")
				}
				if step(KeyEvent{Key: k}) {
					return nil
				}
			}
		case <-escTimer:
			escTimer = nil
			keys, _ := ParseKeys(pending, true)
			wipeBytes(pending)
			pending = nil
			for _, k := range keys {
				if step(KeyEvent{Key: k}) {
					return nil
				}
			}
		case ev := <-events:
			if step(ev) {
				return nil
			}
		}
		// drain whatever else is ready before drawing once
		for drained := false; !drained; {
			select {
			case ev := <-events:
				if step(ev) {
					return nil
				}
			default:
				drained = true
			}
		}
		redraw()
		if m.Quit {
			return nil
		}
	}
}

// runEffect performs one effect against the relay and posts its result.
func runEffect(ctx context.Context, c *api.Client, e Effect, post func(Event), safely func(func())) {
	switch x := e.(type) {
	case PurgeToken:
		c.Wipe()
	case FetchList:
		safely(func() {
			l, err := c.List(ctx)
			post(ListLoaded{Seq: x.Seq, Listing: l, Err: err})
		})
	case FetchWhitelist:
		safely(func() {
			r, err := c.Whitelist(ctx)
			post(WhitelistLoaded{Seq: x.Seq, Rules: r, Err: err})
		})
	case FetchContent:
		safely(func() {
			ct, err := c.Content(ctx, x.ID)
			post(ContentLoaded{ID: x.ID, Content: ct, Err: err})
		})
	case DoApprove:
		safely(func() {
			op, err := OpApprove, error(nil)
			if x.Gated {
				op = OpApproveGated
				err = c.ApproveGated(ctx, x.ID)
			} else {
				err = c.Approve(ctx, x.ID)
			}
			post(ActionDone{Op: op, ID: x.ID, Err: err, ThenWhitelist: x.ThenWhitelist})
		})
	case DoDeny:
		safely(func() { post(ActionDone{Op: OpDeny, ID: x.ID, Err: c.Deny(ctx, x.ID, x.Reason)}) })
	case DoRelease:
		safely(func() { post(ActionDone{Op: OpRelease, ID: x.ID, Err: c.Release(ctx, x.ID)}) })
	case DoAddWhitelist:
		safely(func() {
			post(ActionDone{Op: OpWhitelistAdd, ID: x.ID, Err: c.AddWhitelist(ctx, x.ID), ThenWhitelist: x.AfterApprove})
		})
	case DoRemoveWhitelist:
		safely(func() { post(ActionDone{Op: OpWhitelistRemove, Err: c.RemoveWhitelist(ctx, x.Rule)}) })
	case DoTurbo:
		safely(func() {
			if x.On {
				post(ActionDone{Op: OpTurboOn, Err: c.TurboOn(ctx, 5, 10)})
			} else {
				post(ActionDone{Op: OpTurboOff, Err: c.TurboOff(ctx)})
			}
		})
	case CheckToken:
		safely(func() {
			c.SetToken(x.Token)
			post(TokenChecked{Err: c.Probe(ctx)})
		})
	}
}
