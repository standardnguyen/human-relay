package main

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const hostile = "\x1b[2J\x1b]0;TITLE\x07\x1b]52;c;ZWNobyBoaQ==\x07\x9b31mevil\x00\x7f‮"

func assertNoControlBytes(t *testing.T, what, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' && i == len(s)-1 {
			continue
		}
		if c < 0x20 || c >= 0x7f {
			t.Fatalf("%s carries byte 0x%02x at %d: %q", what, c, i, s)
		}
	}
}

func readerOf(tokens ...string) func() ([]byte, error) {
	i := 0
	return func() ([]byte, error) {
		if i >= len(tokens) {
			return nil, errors.New("no token entered")
		}
		i++
		return []byte(tokens[i-1]), nil
	}
}

func TestStartupErrorFromARelayAnswerShowsNoControlBytes(t *testing.T) {
	for _, status := range []int{500, 502, 418, 200} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(hostile))
		}))
		var stderr bytes.Buffer
		_, err := checkToken(srv.URL, readerOf("sometesttoken"), bufio.NewReader(strings.NewReader("")), &stderr)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: want an error", status)
		}
		var printed bytes.Buffer
		reportError(&printed, err)
		assertNoControlBytes(t, "reported error", printed.String())
		assertNoControlBytes(t, "prompt output", strings.ReplaceAll(stderr.String(), "\n", ""))
		if strings.Contains(printed.String(), "evil") {
			t.Fatalf("the relay's body is not repeated: %q", printed.String())
		}
	}
}

func TestStartupErrorOfAnUnreachableRelayNamesNoProbePath(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	var stderr bytes.Buffer
	_, err = checkToken("http://"+addr, readerOf("sometesttoken"), bufio.NewReader(strings.NewReader("")), &stderr)
	if err == nil {
		t.Fatal("want an error")
	}
	var printed bytes.Buffer
	reportError(&printed, err)
	if strings.Contains(printed.String(), "/api/") || strings.Contains(printed.String(), "approve") {
		t.Fatalf("the probe request is not part of the message: %q", printed.String())
	}
	if !strings.Contains(printed.String(), "refused") && !strings.Contains(printed.String(), "unreachable") {
		t.Fatalf("message = %q", printed.String())
	}
}

func TestReportErrorNeutralisesAnyText(t *testing.T) {
	var b bytes.Buffer
	reportError(&b, errors.New("bad "+hostile))
	assertNoControlBytes(t, "report", b.String())
	if !strings.HasPrefix(b.String(), "human-relay-tui: ") || !strings.Contains(b.String(), `\x1b`) {
		t.Fatalf("report = %q", b.String())
	}
	b.Reset()
	reportError(&b, errors.New(strings.Repeat("long ", 500)))
	if len(b.String()) > 600 {
		t.Fatalf("report is %d bytes", len(b.String()))
	}
}

func TestBadURLIsRejectedBeforeAnyTokenIsRead(t *testing.T) {
	read := func() ([]byte, error) {
		t.Fatal("the token prompt must not appear for a URL that cannot work")
		return nil, nil
	}
	var stderr bytes.Buffer
	for _, u := range []string{"ftp://x", "relay.example:8090", "", "http://"} {
		if _, err := checkToken(u, read, bufio.NewReader(strings.NewReader("")), &stderr); err == nil {
			t.Errorf("%q must be rejected", u)
		}
	}
}

func TestPreflightReportsAnUnreachableRelayBeforeTheTokenPrompt(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if err := preflight(srv.URL); err != nil {
		t.Fatalf("reachable relay: %v", err)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	err := preflight("http://" + addr)
	if err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("closed port: %v", err)
	}
}
