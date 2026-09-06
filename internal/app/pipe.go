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
//   - net.ErrClosed / os.ErrClosed / io.ErrClosedPipe (local TCP/half-close
//     races, or the stdin close used to interrupt a blocked read on shutdown)
//   - quic.ApplicationError with code 0 (graceful remote close, e.g. CloseWithError(0, ...))
func isClosedError(err error) bool {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	var appErr *quic.ApplicationError
	if errors.As(err, &appErr) && appErr.ErrorCode == 0 {
		return true
	}
	return false
}

// isTimeoutError reports whether err is a benign deadline or timeout error,
// which occurs when a tunnel connection hits its stream inactivity window and
// the copy deadlines expire. The tunnel itself remains healthy.
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
//   - plain local close races (net.ErrClosed / os.ErrClosed / io.ErrClosedPipe) → "";
//   - deadline/timeout errors (tunnel inactivity window) → "timeout";
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

// refreshReader wraps a reader and invokes onRead after each read that returned
// data, so a caller can refresh an inactivity watchdog/deadline on real traffic
// rather than on wall-clock time.
type refreshReader struct {
	r      io.Reader
	onRead func()
}

func (rr *refreshReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	if n > 0 && rr.onRead != nil {
		rr.onRead()
	}
	return n, err
}

// copyDirection copies src to dst, treating benign close and timeout errors as
// expected teardown (logged only at debug level) and everything else as an error.
// When a non-nil refresh function is passed, it runs after each read that yields
// data, letting the caller keep an activity-based deadline refreshed on traffic.
func copyDirection(dst io.Writer, src io.Reader, dir string, refresh ...func()) {
	if len(refresh) > 0 && refresh[0] != nil {
		src = &refreshReader{r: src, onRead: refresh[0]}
	}
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

// bidirectionalCopy copies data both ways between a and b until either side
// closes or errors. An optional refresh function is invoked whenever either
// direction reads data, so callers can implement aggregate inactivity tracking.
func bidirectionalCopy(a, b io.ReadWriter, refresh ...func()) {
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

	var rf func()
	if len(refresh) > 0 {
		rf = refresh[0]
	}

	go func() {
		defer wg.Done()
		copyDirection(a, b, "a←b", rf)
		closeBoth()
	}()

	go func() {
		defer wg.Done()
		copyDirection(b, a, "b←a", rf)
		closeBoth()
	}()

	wg.Wait()
}

type stdinOut struct {
	io.Reader
	io.Writer
}

// Close closes os.Stdin to interrupt a blocked stdin read. bidirectionalCopy
// closes both sides when either copy direction ends; without this, the
// stdin→stream goroutine would stay parked in os.Stdin.Read after the peer
// disconnects, since closing the QUIC stream only closes its send side.
func (stdinOut) Close() error {
	return os.Stdin.Close()
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
