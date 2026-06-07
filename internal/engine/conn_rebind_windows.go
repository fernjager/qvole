//go:build windows

package engine

import "syscall"

func rebindControl(network, address string, c syscall.RawConn) error {
	return nil
}
