package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestParseArgsDefaults(t *testing.T) {
	var stderr bytes.Buffer
	c, err := parseArgs(nil, env(map[string]string{"TERM": "xterm-256color"}), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if c.url != "http://127.0.0.1:8090" || c.ascii || !c.bell {
		t.Fatalf("%+v", c)
	}
}

func TestParseArgsFlags(t *testing.T) {
	var stderr bytes.Buffer
	c, err := parseArgs([]string{"--url", "http://relay.example:8090", "--ascii", "--no-bell"}, env(map[string]string{"TERM": "xterm"}), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if c.url != "http://relay.example:8090" || !c.ascii || c.bell {
		t.Fatalf("%+v", c)
	}
}

func TestParseArgsHasNoWayToPassTheToken(t *testing.T) {
	for _, args := range [][]string{{"--token", "x"}, {"-token=x"}, {"--token-file", "f"}, {"--approver-token", "x"}} {
		var stderr bytes.Buffer
		if _, err := parseArgs(args, env(nil), &stderr); err == nil {
			t.Errorf("%v must be rejected", args)
		}
	}
	var stderr bytes.Buffer
	if _, err := parseArgs([]string{"positional"}, env(nil), &stderr); err == nil {
		t.Error("positional arguments are not accepted")
	}
}

func TestDisplayModeEnvironment(t *testing.T) {
	cases := []struct {
		name  string
		flag  bool
		env   map[string]string
		ascii bool
		color bool
		attrs bool
	}{
		{"normal", false, map[string]string{"TERM": "xterm-256color"}, false, true, true},
		{"ascii flag", true, map[string]string{"TERM": "xterm"}, true, false, false},
		{"NO_COLOR", false, map[string]string{"TERM": "xterm", "NO_COLOR": "1"}, false, false, true},
		{"NO_COLOR empty is not set", false, map[string]string{"TERM": "xterm", "NO_COLOR": ""}, false, true, true},
		{"dumb", false, map[string]string{"TERM": "dumb"}, true, false, false},
		{"absent TERM", false, map[string]string{}, true, false, false},
	}
	for _, c := range cases {
		ascii, th := displayMode(c.flag, env(c.env))
		if ascii != c.ascii || th.Color != c.color || th.Attrs != c.attrs {
			t.Errorf("%s: ascii=%v theme=%+v", c.name, ascii, th)
		}
	}
}

func TestPlaintextWarning(t *testing.T) {
	warn := map[string]bool{
		"http://127.0.0.1:8090":      false,
		"http://localhost:8090":      false,
		"http://[::1]:8090":          false,
		"http://127.1.2.3:8090":      false,
		"https://relay.example:8090": false,
		"http://relay.example:8090":  true,
		"http://10.1.2.3:8090":       true,
	}
	for u, want := range warn {
		msg := plaintextWarning(u)
		if (msg != "") != want {
			t.Errorf("%s: warning %q, want warn=%v", u, msg, want)
		}
		if msg != "" && (strings.Contains(msg, "\n") || !strings.Contains(msg, "unencrypted")) {
			t.Errorf("%s: warning should be one line mentioning unencrypted: %q", u, msg)
		}
	}
}

func TestBinaryRefusesToRunWithoutATerminal(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "human-relay-tui")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Stdin = strings.NewReader("mhra_should_never_be_read\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("want exit status 2, got %v", err)
	}
	if !strings.Contains(stderr.String(), "terminal") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "mhra_") {
		t.Fatal("the piped text must never be echoed")
	}
}
