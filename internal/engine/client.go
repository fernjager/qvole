package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/util"
)

const (
	statsReportInterval = 2 * time.Second
	pipeFinishDelay     = 500 * time.Millisecond
)

// RunPipe establishes a peer-to-peer connection and bridges stdin/stdout over QUIC streams.
// If stats is true, throughput is logged to stderr periodically.
func RunPipe(ctx context.Context, relayAddr string, code string, stats bool) error {
	conn, isServer, err := ConnectPeer(ctx, relayAddr, code)
	if err != nil {
		return err
	}
	defer conn.CloseWithError(0, "done")

	role := util.RoleString(isServer)
	util.LogPipe.PrintfSuccess("Connected as %s", role)

	tracker := NewStatsTracker()
	if stats {
		tracker.Start(statsReportInterval)
	}

	stdinTerm := isTerminal(os.Stdin)

	var wg sync.WaitGroup
	var out *quic.Stream

	if !stdinTerm {
		out, err = conn.OpenStreamSync(ctx)
		if err != nil {
			util.LogPipe.PrintfError("Open outbound stream failed: %v", err)
			return err
		}

		var stdinOnce sync.Once
		closeStdin := func() {
			stdinOnce.Do(func() {
				out.Close()
				os.Stdin.Close()
			})
		}

		go func() {
			select {
			case <-conn.Context().Done():
			case <-ctx.Done():
			}
			closeStdin()
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := BufferPool.Get().([]byte)
			defer PutBuffer(buf)
			dst := io.Writer(tracker.TXWriter(out))
			io.CopyBuffer(dst, os.Stdin, buf)
			out.Close()
		}()
	}

	s, acceptErr := conn.AcceptStream(ctx)
	if acceptErr != nil {
		if out != nil {
			out.Close()
		}
		wg.Wait()
		var appErr *quic.ApplicationError
		if errors.As(acceptErr, &appErr) && appErr.ErrorCode == 0 {
			tracker.StopAndLog()
			time.Sleep(pipeFinishDelay)
			return nil
		}
		util.LogPipe.PrintfError("Accept inbound stream failed: %v", acceptErr)
		return acceptErr
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := BufferPool.Get().([]byte)
		defer PutBuffer(buf)
		dst := io.Writer(tracker.RXWriter(os.Stdout))
		io.CopyBuffer(dst, s, buf)
	}()

	wg.Wait()
	tracker.StopAndLog()
	time.Sleep(pipeFinishDelay)
	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
