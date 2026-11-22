// Command human-relay-tui is a terminal approval screen for the relay's web API.
//
// The approver token is typed at a hidden prompt and kept in memory only: it is
// never read from arguments, the environment or a file, and never written
// anywhere.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/standardnguyen/human-relay/tui/api"
	"github.com/standardnguyen/human-relay/tui/safety"
	"github.com/standardnguyen/human-relay/tui/ui"
)

const defaultURL = "http://127.0.0.1:8090"

type config struct {
	url   string
	ascii bool
	bell  bool
}

func parseArgs(args []string, getenv func(string) string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("human-relay-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := config{}
	fs.StringVar(&c.url, "url", defaultURL, "relay web address")
	fs.BoolVar(&c.ascii, "ascii", false, "no colour, ASCII-only glyphs")
	noBell := fs.Bool("no-bell", false, "do not ring the terminal bell for new pending requests")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: human-relay-tui [--url http://host:port] [--ascii] [--no-bell]")
		fmt.Fprintln(stderr, "The approver token is asked for at a hidden prompt; it is never read from arguments or the environment.")
	}
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	c.bell = !*noBell
	return c, nil
}

// displayMode decides glyphs and text styling. --ascii and a missing or dumb
// TERM mean ASCII glyphs with no colour and no text attributes; NO_COLOR keeps
// attributes such as reverse video but drops colour. In every mode the screen
// itself is still drawn with cursor-addressing and alternate-screen sequences,
// so the terminal must understand those.
func displayMode(asciiFlag bool, getenv func(string) string) (bool, ui.Theme) {
	t := getenv("TERM")
	if asciiFlag || t == "" || t == "dumb" {
		return true, ui.Theme{}
	}
	if getenv("NO_COLOR") != "" {
		return false, ui.Theme{Attrs: true}
	}
	return false, ui.Theme{Color: true, Attrs: true}
}

// plaintextWarning is a one-line warning for http URLs that do not point at
// this machine, since the bearer token then crosses the network unencrypted.
func plaintextWarning(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return ""
	}
	host := u.Hostname()
	if host == "localhost" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return fmt.Sprintf("warning: %s is plain http, so the approver token travels unencrypted unless this path is a tunnel such as a private tailnet", u.Host)
}

func main() { os.Exit(run()) }

func run() int {
	harden()
	cfg, err := parseArgs(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		reportError(os.Stderr, err)
		return 2
	}
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		fmt.Fprintln(os.Stderr, "human-relay-tui: needs an interactive terminal (stdin and stdout must be a terminal); the token is only read from a hidden prompt")
		return 2
	}
	if w := plaintextWarning(cfg.url); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}

	if _, err := api.ParseBase(cfg.url); err != nil {
		reportError(os.Stderr, err)
		return 2
	}
	if err := preflight(cfg.url); err != nil {
		reportError(os.Stderr, err)
		return 1
	}
	client, err := askForToken(cfg.url)
	if err != nil {
		reportError(os.Stderr, err)
		return 1
	}
	ascii, theme := displayMode(cfg.ascii, os.Getenv)
	err = ui.Run(context.Background(), ui.RunConfig{
		Client: client,
		Opts:   ui.Options{URL: cfg.url, ASCII: ascii, Bell: cfg.bell, TokenNote: "token ok: approver, or any token on a legacy relay"},
		Theme:  theme,
		In:     os.Stdin,
		Out:    os.Stdout,
	})
	client.Wipe()
	if err != nil {
		reportError(os.Stderr, err)
		return 1
	}
	return 0
}

// maxReport caps one reported message.
const maxReport = 300

// safeText makes any text safe to write to a terminal outside the TUI screen:
// control characters and escape sequences are shown as \x1b-style text,
// non-ASCII characters as <U+XXXX>, and the length is capped.
func safeText(s string) string {
	s = safety.Plain(firstBytes(s, 4*maxReport), safety.Options{ASCII: true})
	if len(s) > maxReport {
		s = s[:maxReport] + "..."
	}
	return s
}

func firstBytes(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// reportError is the one way a startup or exit error reaches stderr.
func reportError(w io.Writer, err error) {
	fmt.Fprintln(w, "human-relay-tui:", safeText(err.Error()))
}

// describeProbeError says why the token check could not be completed. What the
// relay answered is reduced to its status code; the request that was sent is
// not part of the message.
func describeProbeError(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		return fmt.Sprintf("the relay answered HTTP %d", ae.Status)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return "relay unreachable: " + ue.Err.Error()
	}
	if errors.Is(err, api.ErrUnreachable) {
		return "relay unreachable"
	}
	return err.Error()
}

// preflight checks that something is listening at the relay's address, so a
// wrong address is reported before the token is typed.
func preflight(baseURL string) error {
	u, err := api.ParseBase(baseURL)
	if err != nil {
		return err
	}
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) && ne.Err != nil {
			return fmt.Errorf("cannot reach the relay at %s: %v", host, ne.Err)
		}
		return fmt.Errorf("cannot reach the relay at %s: %v", host, err)
	}
	conn.Close()
	return nil
}

// askForToken prompts for the token without echo and checks it once; see
// checkToken for what the check means.
func askForToken(baseURL string) (*api.Client, error) {
	fd := int(os.Stdin.Fd())
	rd := bufio.NewReader(os.Stdin)

	// Ctrl-C at the hidden prompt must not leave the terminal with echo off:
	// ReadPassword restores it only when it returns normally.
	saved, _ := term.GetState(fd)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	defer func() { signal.Stop(sig); close(done) }()
	go func() {
		select {
		case <-sig:
			if saved != nil {
				term.Restore(fd, saved)
			}
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		case <-done:
		}
	}()
	read := func() ([]byte, error) {
		fmt.Fprint(os.Stderr, "approver token (input is hidden): ")
		tok, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return tok, err
	}
	return checkToken(baseURL, read, rd, os.Stderr)
}

// checkToken reads a token with read and checks it by asking the relay to
// approve a request that cannot exist: 404 means the token is accepted for
// decisions (the approver token, or any valid token when the relay has no
// approver token configured), 403 means it is valid but is not the approver
// token, 401 that the relay does not know it. Nothing but fixed text and a
// status code is written about the relay's answer.
func checkToken(baseURL string, read func() ([]byte, error), in *bufio.Reader, stderr io.Writer) (*api.Client, error) {
	if _, err := api.ParseBase(baseURL); err != nil {
		return nil, err
	}
	for {
		tok, err := read()
		if err != nil {
			return nil, errors.New("no token entered")
		}
		client, err := api.New(baseURL, tok)
		if err != nil {
			wipe(tok)
			return nil, err
		}
		if len(strings.TrimSpace(string(tok))) == 0 {
			client.Wipe()
			fmt.Fprintln(stderr, "the token is empty")
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = client.Probe(ctx)
		cancel()
		switch {
		case err == nil:
			return client, nil
		case errors.Is(err, api.ErrForbidden):
			fmt.Fprintln(stderr, "That token is valid, but it is not the relay's approver token (403): it cannot approve, deny or whitelist.")
		case errors.Is(err, api.ErrUnauthorized):
			fmt.Fprintln(stderr, "The relay does not accept that token (401).")
		default:
			client.Wipe()
			return nil, fmt.Errorf("cannot check the token against %s: %s", baseURL, describeProbeError(err))
		}
		client.Wipe()
		fmt.Fprint(stderr, "Enter it again? [Y/n] ")
		line, _ := in.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a == "n" || a == "no" {
			return nil, errors.New("no usable token")
		}
	}
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
