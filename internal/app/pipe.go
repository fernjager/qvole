package app

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
)

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
		if _, err := io.CopyBuffer(a, b, buf); err != nil {
			util.LogCopy.PrintfError("Copy error (a←b): %v", err)
		}
		closeBoth()
	}()

	go func() {
		defer wg.Done()
		buf := engine.BufferPool.Get().([]byte)
		defer engine.PutBuffer(buf)
		if _, err := io.CopyBuffer(b, a, buf); err != nil {
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
