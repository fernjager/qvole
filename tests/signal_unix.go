//go:build !windows

package tests

import (
	"os"
	"syscall"
)

func gracefulSignal() os.Signal {
	return syscall.SIGTERM
}
