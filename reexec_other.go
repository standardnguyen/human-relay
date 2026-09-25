//go:build !unix

package main

// scrubPlaintextApproverToken is a no-op off unix: there is no
// /proc/<pid>/environ to scrub, and no exec that keeps the process.
func scrubPlaintextApproverToken() {}
