//go:build unix

package main

import "syscall"

// harden asks the system not to write a core file, so the process memory that
// holds the token cannot end up on disk after a crash. It is best effort.
func harden() {
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0})
	denyDebuggers()
}
