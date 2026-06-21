//go:build !windows

package app

import "syscall"

// childSysProcAttr returns a SysProcAttr for the child process.
// Linux Pdeathsig would prevent orphans on OOM/SIGKILL but is not
// available on macOS.
func childSysProcAttr() *syscall.SysProcAttr {
	return nil
}
