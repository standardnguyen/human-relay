//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"strings"
	"syscall"
)

// scrubPlaintextApproverToken moves a plaintext MHR_APPROVER_TOKEN out of the
// process's initial environment.
//
// os.Unsetenv only edits Go's copy of the environment. The kernel keeps the
// block the process was exec'd with and serves it at /proc/<pid>/environ, and
// every command the relay runs executes as the relay's uid, so any approved or
// whitelisted command could read the approver token from there and approve on
// its own from then on. Re-exec'ing this binary with the plaintext replaced by
// its SHA-256 (MHR_APPROVER_TOKEN_SHA256) replaces that block: the relay only
// ever needs the digest, and the PID is unchanged, so a supervisor (systemd,
// docker's PID 1) notices nothing.
//
// It runs first thing in main, before any goroutine, listener or command, so
// the only window in which the plaintext is readable is before the relay can
// run anything. Setting both variables is left to approverDigestFromEnv to
// refuse.
func scrubPlaintextApproverToken() {
	token := os.Getenv("MHR_APPROVER_TOKEN")
	if token == "" || os.Getenv("MHR_APPROVER_TOKEN_SHA256") != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("cannot locate this binary to drop MHR_APPROVER_TOKEN from the process environment (%v); set MHR_APPROVER_TOKEN_SHA256 instead", err)
	}
	sum := sha256.Sum256([]byte(token))
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "MHR_APPROVER_TOKEN=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "MHR_APPROVER_TOKEN_SHA256="+hex.EncodeToString(sum[:]))
	err = syscall.Exec(exe, os.Args, env)
	// Exec only returns on failure. Refuse to run with the plaintext still
	// readable rather than carry on quietly.
	log.Fatalf("re-exec to drop MHR_APPROVER_TOKEN from the process environment failed (%v); set MHR_APPROVER_TOKEN_SHA256 instead", err)
}
