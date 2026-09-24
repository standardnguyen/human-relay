package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	approverTokenVar  = "MHR_APPROVER_TOKEN"
	approverDigestVar = "MHR_APPROVER_TOKEN_SHA256"
)

// initialEnviron returns the environment block this process was exec'd with,
// duplicates included. os.Environ() cannot be used to look for duplicates: at
// startup Go keeps the first entry of each key and blanks the rest, so a
// second MHR_APPROVER_TOKEN_SHA256 entry is invisible to it. On Linux the
// kernel's copy at /proc/self/environ still has every entry; elsewhere this
// falls back to os.Environ().
func initialEnviron() []string {
	raw, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		return os.Environ()
	}
	var out []string
	for _, kv := range strings.Split(string(raw), "\x00") {
		if kv != "" {
			out = append(out, kv)
		}
	}
	return out
}

// approverEnv is the approver configuration found in one environment block.
type approverEnv struct {
	token     string // MHR_APPROVER_TOKEN, "" when absent or empty
	digestHex string // MHR_APPROVER_TOKEN_SHA256, "" when absent or empty
}

// readApproverEnv reads the approver variables from a raw environment block:
// initialEnviron(), or the block a re-exec is about to hand the kernel.
//
// It reads every entry rather than calling os.Getenv, because Go's Getenv
// returns the FIRST entry for a key while other programs may take the last:
// with two entries for one variable, which one "is" the setting depends on who
// asks. That ambiguity is how an empty MHR_APPROVER_TOKEN_SHA256= line ahead of
// the real digest once switched the approver gate off, so a variable that
// appears more than once is an error, whatever the values.
func readApproverEnv(environ []string) (approverEnv, error) {
	var e approverEnv
	seen := map[string]int{}
	for _, kv := range environ {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || (key != approverTokenVar && key != approverDigestVar) {
			continue
		}
		seen[key]++
		if seen[key] > 1 {
			return approverEnv{}, fmt.Errorf("%s appears more than once in the environment; set it once, so there is no question which value the relay uses", key)
		}
		if key == approverTokenVar {
			e.token = val
		} else {
			e.digestHex = val
		}
	}
	return e, nil
}

// approverValuePresent reports whether any entry of either approver variable
// carries a non-empty value. main refuses to start in legacy mode when it
// does: an operator who configured an approver must never get a relay where
// every agent token approves.
func approverValuePresent(environ []string) bool {
	for _, kv := range environ {
		key, val, ok := strings.Cut(kv, "=")
		if ok && val != "" && (key == approverTokenVar || key == approverDigestVar) {
			return true
		}
	}
	return false
}

// scrubbedApproverEnv returns environ with every entry of either approver
// variable removed and a single MHR_APPROVER_TOKEN_SHA256=<digestHex> appended.
// Removing every digest entry, empty ones included, is the point: an entry left
// behind would come first and win.
func scrubbedApproverEnv(environ []string, digestHex string) []string {
	out := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		if strings.HasPrefix(kv, approverTokenVar+"=") || strings.HasPrefix(kv, approverDigestVar+"=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, approverDigestVar+"="+digestHex)
}
