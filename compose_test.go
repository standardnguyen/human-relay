package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Approved commands run as root inside the relay's own container. With NET_RAW
// (in Docker's default capability set) a command can open a packet socket and
// read the dashboard's HTTP traffic, including the approver's Authorization
// header, then approve its own requests. The shipped compose file must drop it.
func TestComposeDropsNetRaw(t *testing.T) {
	const file = "docker-compose.yml"
	const service = "human-relay"
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	drops, err := composeCapList(string(b), service, "cap_drop")
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	if !hasCap(drops, "NET_RAW") && !hasCap(drops, "ALL") {
		t.Fatalf("%s: service %q must list NET_RAW under cap_drop (it drops %v); without it an approved command can open a packet socket and read the approver credential off the wire", file, service, drops)
	}
	adds, err := composeCapList(string(b), service, "cap_add")
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	if hasCap(adds, "NET_RAW") || hasCap(adds, "ALL") {
		t.Fatalf("%s: service %q adds NET_RAW back under cap_add (%v)", file, service, adds)
	}
}

func TestComposeCapList(t *testing.T) {
	cases := []struct {
		name    string
		compose string
		want    []string
		wantErr string
	}{
		{"flow", "services:\n  human-relay:\n    build: .\n    cap_drop: [NET_RAW, \"SYS_PTRACE\"]\n", []string{"NET_RAW", "SYS_PTRACE"}, ""},
		{"block", "services:\n  human-relay:\n    cap_drop:\n      - NET_RAW # packet sockets\n    restart: always\n", []string{"NET_RAW"}, ""},
		{"block-same-indent", "services:\n  human-relay:\n    cap_drop:\n    - 'CAP_NET_RAW'\n", []string{"NET_RAW"}, ""},
		{"absent", "services:\n  human-relay:\n    build: .\n", nil, ""},
		{"only-in-comment", "services:\n  human-relay:\n    build: .\n    # cap_drop: [NET_RAW]\n", nil, ""},
		{"other-service", "services:\n  signal-api:\n    cap_drop: [NET_RAW]\n  human-relay:\n    build: .\n", nil, ""},
		{"nested-key", "services:\n  human-relay:\n    labels:\n      cap_drop: NET_RAW\n", nil, ""},
		{"outside-services", "volumes:\n  human-relay:\n    cap_drop: [NET_RAW]\nservices:\n  human-relay:\n    build: .\n", nil, ""},
		{"service-missing", "services:\n  relay:\n    cap_drop: [NET_RAW]\n", nil, `service "human-relay" not found`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := composeCapList(tc.compose, "human-relay", "cap_drop")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// composeCapList returns the capabilities listed under key (cap_drop or
// cap_add) in one service of a compose file, upper-cased and without a CAP_
// prefix. go.mod carries no YAML parser, so this reads only the two list
// shapes compose accepts for these keys: a flow list (key: [A, B]) and a block
// list (- A lines under the key).
func composeCapList(compose, service, key string) ([]string, error) {
	var caps []string
	inServices, inService, inKey, found := false, false, false, false
	svcIndent, fieldIndent := -1, -1
	for _, raw := range strings.Split(compose, "\n") {
		line := strings.TrimRight(stripYAMLComment(raw), " \t\r")
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		ind := len(line) - len(strings.TrimLeft(line, " "))
		if ind == 0 {
			inServices, inService, inKey = text == "services:", false, false
			continue
		}
		if !inServices {
			continue
		}
		if svcIndent < 0 {
			svcIndent = ind
		}
		if ind <= svcIndent {
			inService, inKey, fieldIndent = text == service+":", false, -1
			found = found || inService
			continue
		}
		if !inService {
			continue
		}
		if fieldIndent < 0 {
			fieldIndent = ind
		}
		if inKey && ind >= fieldIndent && strings.HasPrefix(text, "- ") {
			caps = append(caps, normCap(strings.TrimPrefix(text, "- ")))
			continue
		}
		if ind > fieldIndent {
			continue
		}
		inKey = false
		k, v, ok := strings.Cut(text, ":")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case v == "":
			inKey = true
		case strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]"):
			for _, c := range strings.Split(v[1:len(v)-1], ",") {
				if c = strings.TrimSpace(c); c != "" {
					caps = append(caps, normCap(c))
				}
			}
		default:
			return nil, fmt.Errorf("service %q: %s is not a list: %q", service, key, v)
		}
	}
	if !found {
		return nil, fmt.Errorf("service %q not found under services:", service)
	}
	return caps, nil
}

func normCap(s string) string {
	s = strings.ToUpper(strings.Trim(strings.TrimSpace(s), `"'`))
	return strings.TrimPrefix(s, "CAP_")
}

// stripYAMLComment drops a # comment, which YAML starts only at the beginning
// of a line or after whitespace.
func stripYAMLComment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
			return s[:i]
		}
	}
	return s
}
