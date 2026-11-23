package api_test

import (
	"reflect"
	"testing"

	"github.com/standardnguyen/human-relay/tui/api"
)

func TestWhitelistKeyPerType(t *testing.T) {
	cases := []struct {
		name string
		req  api.Request
		cmd  string
		args []string
		ok   bool
	}{
		{"command", api.Request{Command: "ls", Args: []string{"-l", "/"}}, "ls", []string{"-l", "/"}, true},
		{"command no args", api.Request{Command: "ls"}, "ls", []string{}, true},
		{"http", api.Request{Type: "http", HTTPMethod: "POST", HTTPURL: "https://x/y", HTTPBody: "b"}, "POST", []string{"https://x/y"}, true},
		{"script", api.Request{Type: "script", ScriptName: "backup"}, "run_script", []string{"backup"}, true},
		{"script_create", api.Request{Type: "script_create", ScriptName: "s", StdinSHA256: "abc"}, "create_script", []string{"s", "abc"}, true},
		{"create_then_run", api.Request{Type: "script_create_then_run", ScriptName: "s", StdinSHA256: "abc"}, "create_then_run", []string{"s", "abc"}, true},
		{"create_then_run empty body", api.Request{Type: "script_create_then_run", ScriptName: "s"}, "create_then_run", []string{"s", ""}, true},
		{"registry never", api.Request{Type: "registry_op", RegistryOp: "register_container"}, "", nil, false},
	}
	for _, c := range cases {
		cmd, args, ok := api.WhitelistKey(c.req)
		if cmd != c.cmd || ok != c.ok || !reflect.DeepEqual(args, c.args) {
			t.Errorf("%s: got %q %q %v, want %q %q %v", c.name, cmd, args, ok, c.cmd, c.args, c.ok)
		}
	}
}

func TestRuleMatching(t *testing.T) {
	rules := []api.Rule{{Command: "ls", Args: []string{"-l"}}, {Command: "echo", Args: nil}}
	if !api.HasRule(rules, "ls", []string{"-l"}) {
		t.Error("exact match")
	}
	if api.HasRule(rules, "ls", []string{"-l", "x"}) || api.HasRule(rules, "ls", nil) {
		t.Error("args must match exactly")
	}
	if !api.HasRule(rules, "echo", []string{}) || !api.HasRule(rules, "echo", nil) {
		t.Error("nil and empty args are the same")
	}
	if api.HasRule(rules, "rm", nil) {
		t.Error("unknown command")
	}
}
