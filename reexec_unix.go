//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
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
// run anything. An environment it cannot read unambiguously (a variable set
// twice) or that sets both variables is left untouched for
// approverDigestFromEnv to refuse.
//
// The re-exec environment drops EVERY entry of both variables, empty ones
// included, before appending the digest. An empty MHR_APPROVER_TOKEN_SHA256=
// left in place would sit ahead of the appended digest, Go would read the
// empty one, and the relay would start in legacy mode where every agent token
// approves.
func scrubPlaintextApproverToken() {
	environ := initialEnviron()
	e, err := readApproverEnv(environ)
	if err != nil || e.token == "" || e.digestHex != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("cannot locate this binary to drop MHR_APPROVER_TOKEN from the process environment (%v); set MHR_APPROVER_TOKEN_SHA256 instead", err)
	}
	sum := sha256.Sum256([]byte(e.token))
	err = syscall.Exec(exe, os.Args, scrubbedApproverEnv(environ, hex.EncodeToString(sum[:])))
	// Exec only returns on failure. Refuse to run with the plaintext still
	// readable rather than carry on quietly.
	log.Fatalf("re-exec to drop MHR_APPROVER_TOKEN from the process environment failed (%v); set MHR_APPROVER_TOKEN_SHA256 instead", err)
}
