//go:build linux

package main

import "syscall"

const prSetDumpable = 4 // PR_SET_DUMPABLE

// denyDebuggers marks the process non-dumpable: no core file, and processes of
// other users cannot attach to it or read its memory through /proc.
func denyDebuggers() {
	syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0)
}
