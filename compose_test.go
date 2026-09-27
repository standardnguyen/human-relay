package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Commands the relay runs in its own container execute there as root. With
// NET_RAW (in Docker's default capability set) such a command can open a packet
// socket and read the dashboard's HTTP traffic, including the approver's
// Authorization header, then approve its own requests. The shipped compose file
// must drop it.
func TestComposeDropsNetRaw(t *testing.T) {
	const file = "docker-compose.yml"
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNetRawDropped(string(b), "human-relay"); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}

func TestCheckNetRawDropped(t *testing.T) {
	const head = "services:\n  human-relay:\n    build: .\n"
	const drop = "    cap_drop:\n      - NET_RAW\n"
	const anchor = "x-caps:\n  raw: &raw NET_RAW\n"
	// unresolved is the start of the error for a list item this check cannot
	// read as a plain capability name.
	unresolved := func(key, item string) string {
		return fmt.Sprintf("service %q: %s item %q: cannot resolve", "human-relay", key, item)
	}
	cases := []struct {
		name    string
		compose string
		wantErr string
	}{
		{"drops", head + drop, ""},
		{"drops-all", head + "    cap_drop: [ALL]\n", ""},
		{"no-drop", head, "must list NET_RAW under cap_drop"},
		{"cap-add", head + drop + "    cap_add: [NET_RAW]\n", "adds NET_RAW back under cap_add"},
		{"cap-add-all", head + drop + "    cap_add:\n      - ALL\n", "adds NET_RAW back under cap_add"},
		{"cap-add-quoted-key", head + drop + "    \"cap_add\": [NET_RAW]\n", "adds NET_RAW back under cap_add"},
		{"privileged-true", head + drop + "    privileged: true\n", "privileged"},
		{"privileged-yes", head + drop + "    privileged: Yes\n", "privileged"},
		{"privileged-quoted", head + drop + "    privileged: \"on\"\n", "privileged"},
		{"privileged-unreadable", head + drop + "    privileged: ${RELAY_PRIVILEGED}\n", "privileged"},
		{"privileged-false", head + drop + "    privileged: false\n", ""},
		{"privileged-no", head + drop + "    privileged: no # never\n", ""},
		{"privileged-other-service", "services:\n  signal-api:\n    privileged: true\n  human-relay:\n    build: .\n" + drop, ""},
		{"extends", head + drop + "    extends:\n      service: base\n", "extends"},
		{"extends-flow", head + drop + "    extends: {service: base}\n", "extends"},
		{"merge-key", "x-extra: &extra\n  cap_add: [NET_RAW]\nservices:\n  human-relay:\n    <<: *extra\n    build: .\n" + drop, "<<"},
		{"merge-key-list", "services:\n  human-relay:\n    <<: [*a, *b]\n" + drop, "<<"},
		// Items docker compose config resolves (aliases, interpolation, tags,
		// block scalars) but this parser cannot, so they fail closed.
		{"cap-add-alias-flow", anchor + head + drop + "    cap_add: [*raw]\n", unresolved("cap_add", "*raw")},
		{"cap-add-alias-block", anchor + head + drop + "    cap_add:\n      - *raw\n", unresolved("cap_add", "*raw")},
		{"cap-drop-alias-flow", anchor + head + "    cap_drop: [NET_RAW, *raw]\n", unresolved("cap_drop", "*raw")},
		{"cap-drop-alias-block", anchor + head + drop + "      - *raw\n", unresolved("cap_drop", "*raw")},
		{"cap-add-interp-flow", head + drop + "    cap_add: [\"${EXTRA_CAP:-NET_RAW}\"]\n", unresolved("cap_add", "${EXTRA_CAP:-NET_RAW}")},
		{"cap-add-interp-block", head + drop + "    cap_add:\n      - ${EXTRA_CAP}\n", unresolved("cap_add", "${EXTRA_CAP}")},
		{"cap-add-interp-default-block", head + drop + "    cap_add:\n      - ${EXTRA_CAP:-NET_RAW}\n", unresolved("cap_add", "${EXTRA_CAP:-NET_RAW}")},
		{"cap-add-interp-bare-block", head + drop + "    cap_add:\n      - $EXTRA_CAP\n", unresolved("cap_add", "$EXTRA_CAP")},
		{"cap-drop-interp-flow", head + "    cap_drop: [NET_RAW, \"${EXTRA_DROP}\"]\n", unresolved("cap_drop", "${EXTRA_DROP}")},
		{"cap-drop-interp-block", head + drop + "      - ${EXTRA_DROP}\n", unresolved("cap_drop", "${EXTRA_DROP}")},
		{"cap-add-anchored-item", head + drop + "    cap_add: [&a NET_RAW]\n", unresolved("cap_add", "&a NET_RAW")},
		{"cap-add-tagged-item", head + drop + "    cap_add:\n      - !!str NET_RAW\n", unresolved("cap_add", "!!str NET_RAW")},
		{"cap-add-block-scalar", head + drop + "    cap_add:\n      - >-\n        NET_RAW\n", unresolved("cap_add", ">-")},
		{"cap-add-bare-dash", head + drop + "    cap_add:\n      -\n        NET_RAW\n", unresolved("cap_add", "")},
		{"cap-add-bare-dash-same-indent", head + drop + "    cap_add:\n    -\n      NET_RAW\n", unresolved("cap_add", "")},
		{"cap-drop-continued-item", head + drop + "        EXTRA\n", unresolved("cap_drop", "EXTRA")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNetRawDropped(tc.compose, "human-relay")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

// checkNetRawDropped reports why a service in a compose file would still hold
// NET_RAW, or nil if it drops it.
func checkNetRawDropped(compose, service string) error {
	keys, err := serviceKeys(compose, service)
	if err != nil {
		return err
	}
	for _, k := range []string{"extends", "<<"} {
		if _, ok := keys[k]; ok {
			return fmt.Errorf("service %q uses %s:, which this check cannot resolve; check by hand (docker compose config) that the service drops NET_RAW, does not add it back and is not privileged", service, k)
		}
	}
	if v, ok := keys["privileged"]; ok {
		switch strings.ToLower(strings.Trim(v, `"'`)) {
		case "false", "no", "off":
		default:
			return fmt.Errorf("service %q sets privileged: %q, and a privileged container ignores cap_drop and holds NET_RAW again; only false, no or off pass", service, v)
		}
	}
	drops, err := composeCapList(compose, service, "cap_drop")
	if err != nil {
		return err
	}
	if !hasCap(drops, "NET_RAW") && !hasCap(drops, "ALL") {
		return fmt.Errorf("service %q must list NET_RAW under cap_drop (it drops %v); without it a command run in the relay's container can open a packet socket and read the approver credential off the wire", service, drops)
	}
	adds, err := composeCapList(compose, service, "cap_add")
	if err != nil {
		return err
	}
	if hasCap(adds, "NET_RAW") || hasCap(adds, "ALL") {
		return fmt.Errorf("service %q adds NET_RAW back under cap_add (%v)", service, adds)
	}
	return nil
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
		{"quoted-key", "services:\n  human-relay:\n    'cap_drop': [NET_RAW]\n", []string{"NET_RAW"}, ""},
		{"multi-line-flow", "services:\n  human-relay:\n    cap_drop: [\n      NET_RAW\n    ]\n", nil, "not in a list shape this parser reads"},
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

type yamlLine struct {
	ind  int
	text string
}

// serviceBody returns the lines of one service in a compose file, without
// comments or blank lines, and the indentation of the service's own keys.
// go.mod carries no YAML parser, so this goes by indentation alone and does
// not resolve anchors, aliases, merge keys or extends.
func serviceBody(compose, service string) ([]yamlLine, int, error) {
	var body []yamlLine
	inServices, inService, found := false, false, false
	svcIndent := -1
	for _, raw := range strings.Split(compose, "\n") {
		line := strings.TrimRight(stripYAMLComment(raw), " \t\r")
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		ind := len(line) - len(strings.TrimLeft(line, " "))
		if ind == 0 {
			inServices, inService = text == "services:", false
			continue
		}
		if !inServices {
			continue
		}
		if svcIndent < 0 {
			svcIndent = ind
		}
		if ind <= svcIndent {
			inService = text == service+":"
			found = found || inService
			continue
		}
		if inService {
			body = append(body, yamlLine{ind, text})
		}
	}
	if !found {
		return nil, 0, fmt.Errorf("service %q not found under services:", service)
	}
	fieldIndent := -1
	if len(body) > 0 {
		fieldIndent = body[0].ind
	}
	return body, fieldIndent, nil
}

// cutKey splits a "key: value" line into its unquoted key and trimmed value.
func cutKey(text string) (string, string, bool) {
	k, v, ok := strings.Cut(text, ":")
	return strings.Trim(strings.TrimSpace(k), `"'`), strings.TrimSpace(v), ok
}

// serviceKeys maps each of one service's own keys to the rest of its line:
// the inline value, or "" when the value is a nested block.
func serviceKeys(compose, service string) (map[string]string, error) {
	body, fieldIndent, err := serviceBody(compose, service)
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, l := range body {
		if l.ind > fieldIndent || strings.HasPrefix(l.text, "- ") {
			continue
		}
		if k, v, ok := cutKey(l.text); ok {
			keys[k] = v
		}
	}
	return keys, nil
}

// composeCapList returns the capabilities listed under key (cap_drop or
// cap_add) in one service of a compose file, upper-cased and without a CAP_
// prefix. It reads only the two list shapes this parser supports: a
// single-line flow list (key: [A, B]) and a block list (- A lines under the
// key). Any other shape is an error rather than an empty list, and so is any
// item that is not a plain capability name: an alias, anchor, tag, ${..}
// interpolation or multi-line value can resolve to NET_RAW without saying so.
func composeCapList(compose, service, key string) ([]string, error) {
	body, fieldIndent, err := serviceBody(compose, service)
	if err != nil {
		return nil, err
	}
	var caps []string
	unresolved := func(item string) error {
		return fmt.Errorf("service %q: %s item %q: cannot resolve (an alias, anchor, tag, interpolation or multi-line value); check by hand with docker compose config that the service drops NET_RAW and does not add it back", service, key, strings.Trim(strings.TrimSpace(item), `"'`))
	}
	add := func(item string) error {
		c := normCap(item)
		if !isPlainCap(c) {
			return unresolved(item)
		}
		caps = append(caps, c)
		return nil
	}
	inKey := false
	for _, l := range body {
		if inKey && l.ind >= fieldIndent && (l.text == "-" || strings.HasPrefix(l.text, "- ")) {
			if err := add(strings.TrimPrefix(l.text, "-")); err != nil {
				return nil, err
			}
			continue
		}
		if inKey && l.ind > fieldIndent {
			// A deeper line that is not an item continues the one above it.
			return nil, unresolved(l.text)
		}
		if l.ind > fieldIndent {
			continue
		}
		inKey = false
		k, v, ok := cutKey(l.text)
		if !ok || k != key {
			continue
		}
		switch {
		case v == "":
			inKey = true
		case strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]"):
			for _, c := range strings.Split(v[1:len(v)-1], ",") {
				if c = strings.TrimSpace(c); c != "" {
					if err := add(c); err != nil {
						return nil, err
					}
				}
			}
		default:
			return nil, fmt.Errorf("service %q: %s is not in a list shape this parser reads (single-line [..] or block - items): %q", service, key, v)
		}
	}
	return caps, nil
}

func normCap(s string) string {
	s = strings.ToUpper(strings.Trim(strings.TrimSpace(s), `"'`))
	return strings.TrimPrefix(s, "CAP_")
}

// isPlainCap reports whether a normalised item is a bare capability name
// (letters, digits and underscores), the only item shape read at face value.
func isPlainCap(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
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
