//go:build windows

package app

import "syscall"

// childSysProcAttr returns nil on Windows (no Pdeathsig equivalent).
func childSysProcAttr() *syscall.SysProcAttr {
	return nil
}
