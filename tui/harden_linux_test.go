//go:build linux

package main

import (
	"syscall"
	"testing"
)

func TestHardenDisablesCoreFilesAndDumping(t *testing.T) {
	var before syscall.Rlimit
	syscall.Getrlimit(syscall.RLIMIT_CORE, &before)
	t.Cleanup(func() {
		syscall.Setrlimit(syscall.RLIMIT_CORE, &before)
		syscall.RawSyscall(syscall.SYS_PRCTL, 4, 1, 0)
	})
	harden()
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &lim); err != nil || lim.Cur != 0 {
		t.Fatalf("core limit %+v, %v", lim, err)
	}
	dumpable, _, _ := syscall.RawSyscall(syscall.SYS_PRCTL, 3, 0, 0) // PR_GET_DUMPABLE
	if dumpable != 0 {
		t.Fatalf("dumpable = %d", dumpable)
	}
}
