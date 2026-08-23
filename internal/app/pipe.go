package app

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
)

// isClosedError reports whether err is a benign error caused by one side of a
// bidirectional copy closing the other during shutdown. This includes:
//   - net.ErrClosed / io.ErrClosedPipe (local TCP/half-close races)
//   - quic.ApplicationError with code 0 (graceful remote close, e.g. CloseWithError(0, ...))
func isClosedError(err error) bool {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	var appErr *quic.ApplicationError
	if errors.As(err, &appErr) && appErr.ErrorCode == 0 {
		return true
	}
	return false
}

// isTimeoutError reports whether err is a benign deadline or timeout error,
// which occurs when a tunnel connection hits its absolute lifetime cap and the
// copy deadlines expire. The tunnel itself remains healthy.
func isTimeoutError(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// copyErrorKind classifies a copy error for logging: "" means benign teardown
// (no log), otherwise one of "timeout", "disconnect", or "error".
//   - graceful remote close (quic.ApplicationError with code 0) → "" (normal
//     end of session, e.g. CloseWithError(0, "done"));
//   - peer disconnect (non-zero application error, idle timeout, stateless
//     reset, transport error) → "disconnect", logged at debug level;
//   - plain local close races (net.ErrClosed / io.ErrClosedPipe) → "";
//   - deadline/timeout errors (tunnel lifetime cap) → "timeout";
//   - everything else → "error".
func copyErrorKind(err error) string {
	if err == nil {
		return ""
	}
	var appErr *quic.ApplicationError
	if errors.As(err, &appErr) && appErr.ErrorCode == 0 {
		return "" // graceful remote close
	}
	if engine.IsPeerDisconnect(err) {
		return "disconnect"
	}
	if isClosedError(err) {
		return "" // benign local close race during shutdown
	}
	if isTimeoutError(err) {
		return "timeout"
	}
	return "error"
}

// copyDirection copies src to dst, treating benign close and timeout errors as
// expected teardown (logged only at debug level) and everything else as an error.
func copyDirection(dst, src io.ReadWriter, dir string) {
	buf := engine.BufferPool.Get().([]byte)
	defer engine.PutBuffer(buf)
	if _, err := io.CopyBuffer(dst, src, buf); err != nil {
		switch copyErrorKind(err) {
		case "timeout":
			util.LogCopy.Printf("Copy timeout (%s): %v", dir, err)
		case "disconnect":
			util.LogCopy.Printf("Copy disconnect (%s): %v", dir, err)
		case "error":
			util.LogCopy.PrintfError("Copy error (%s): %v", dir, err)
		}
	}
}

func bidirectionalCopy(a, b io.ReadWriter) {
	var wg sync.WaitGroup
	wg.Add(2)

	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			if c, ok := a.(io.Closer); ok {
				c.Close()
			}
			if c, ok := b.(io.Closer); ok {
				c.Close()
			}
		})
	}

	go func() {
		defer wg.Done()
		copyDirection(a, b, "a←b")
		closeBoth()
	}()

	go func() {
		defer wg.Done()
		copyDirection(b, a, "b←a")
		closeBoth()
	}()

	wg.Wait()
}

type stdinOut struct {
	io.Reader
	io.Writer
}

// StartStdinPipe bridges a stream (rwc) to stdin/stdout, handling cleanup on context cancellation.
func StartStdinPipe(ctx context.Context, rwc io.ReadWriteCloser) {
	var stdinOnce sync.Once
	stdinCloser := func() {
		stdinOnce.Do(func() {
			rwc.Close()
			os.Stdin.Close()
		})
	}

	go func() {
		<-ctx.Done()
		stdinCloser()
	}()

	bidirectionalCopy(stdinOut{os.Stdin, os.Stdout}, rwc)
}
