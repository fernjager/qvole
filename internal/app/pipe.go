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
		buf := engine.BufferPool.Get().([]byte)
		defer engine.PutBuffer(buf)
		if _, err := io.CopyBuffer(a, b, buf); err != nil && !isClosedError(err) {
			util.LogCopy.PrintfError("Copy error (a←b): %v", err)
		}
		closeBoth()
	}()

	go func() {
		defer wg.Done()
		buf := engine.BufferPool.Get().([]byte)
		defer engine.PutBuffer(buf)
		if _, err := io.CopyBuffer(b, a, buf); err != nil && !isClosedError(err) {
			util.LogCopy.PrintfError("Copy error (b←a): %v", err)
		}
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
