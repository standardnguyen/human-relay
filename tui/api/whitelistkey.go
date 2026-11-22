package api

// WhitelistKey is the rule a request would be whitelisted under, derived the
// way the relay derives it. ok is false for requests that cannot be
// whitelisted (registry operations have no command to match on).
func WhitelistKey(r Request) (command string, args []string, ok bool) {
	switch r.Type {
	case TypeHTTP:
		return r.HTTPMethod, []string{r.HTTPURL}, true
	case TypeScript:
		return "run_script", []string{r.ScriptName}, true
	case TypeScriptCreate:
		return "create_script", []string{r.ScriptName, r.StdinSHA256}, true
	case TypeScriptCreateRun:
		return "create_then_run", []string{r.ScriptName, r.StdinSHA256}, true
	case TypeRegistryOp:
		return "", nil, false
	}
	args = r.Args
	if args == nil {
		args = []string{}
	}
	return r.Command, args, r.Command != ""
}

// HasRule reports whether rules holds an entry with exactly this command and
// args. A rule with no args and one with an empty list are the same.
func HasRule(rules []Rule, command string, args []string) bool {
	for _, ru := range rules {
		if ru.Command != command || len(ru.Args) != len(args) {
			continue
		}
		same := true
		for i := range args {
			if ru.Args[i] != args[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}
