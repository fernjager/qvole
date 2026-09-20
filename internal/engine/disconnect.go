package engine

import (
	"context"
	"errors"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/util"
)

// IsPeerDisconnect reports whether err means the remote peer closed or lost the
// connection (application error, idle timeout, stateless reset, or transport
// error), as opposed to a local cancellation or an unexpected local failure.
func IsPeerDisconnect(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var appErr *quic.ApplicationError
	var idleErr *quic.IdleTimeoutError
	var resetErr *quic.StatelessResetError
	var transErr *quic.TransportError
	return errors.As(err, &appErr) ||
		errors.As(err, &idleErr) ||
		errors.As(err, &resetErr) ||
		errors.As(err, &transErr)
}

// LogDisconnect logs a friendly "Remote host disconnected" message, keeping the
// underlying error detail for --debug. It returns true when err was a peer
// disconnect (and a message was logged).
func LogDisconnect(l *util.Logger, err error) bool {
	if !IsPeerDisconnect(err) {
		return false
	}
	if util.Debug.Load() {
		l.PrintfWarn("Remote host disconnected: %v", err)
	} else {
		l.PrintfWarn("Remote host disconnected")
	}
	return true
}
