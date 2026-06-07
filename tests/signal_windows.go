//go:build windows

package tests

import "os"

func gracefulSignal() os.Signal {
	return os.Kill
}
