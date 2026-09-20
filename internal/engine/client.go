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
	// pipeCloseGrace is how long the local stdin→stream copy may keep sending
	// after the peer closed its stream (half-close), before RunPipe closes
	// the outbound stream and returns.
	pipeCloseGrace = 5 * time.Second
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
	// Teardown is unconditional: every return path must stop the periodic
	// reporter, and the finish delay must elapse before the deferred
	// conn.CloseWithError fires (CONNECTION_CLOSE does not guarantee delivery
	// of unacknowledged stream data). StopAndLog is idempotent.
	defer func() {
		tracker.StopAndLog()
		time.Sleep(pipeFinishDelay)
	}()

	stdinTerm := isTerminal(os.Stdin)

	var out *quic.Stream
	var closeStdin func()
	var stdinDone chan struct{}

	if !stdinTerm {
		out, err = conn.OpenStreamSync(ctx)
		if err != nil {
			util.LogPipe.PrintfError("Open outbound stream failed: %v", err)
			return err
		}

		var stdinOnce sync.Once
		closeStdin = func() {
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

		// The stdin copy runs detached: a stdin read blocked on a pipe cannot
		// be interrupted by os.Stdin.Close on all platforms, so RunPipe must
		// not wait for this goroutine once the peer is done (see below).
		stdinDone = make(chan struct{})
		go func() {
			defer close(stdinDone)
			buf := BufferPool.Get().([]byte)
			defer PutBuffer(buf)
			dst := io.Writer(tracker.TXWriter(out))
			io.CopyBuffer(dst, os.Stdin, buf)
			closeStdin()
		}()
	}

	s, acceptErr := conn.AcceptStream(ctx)
	if acceptErr != nil {
		if closeStdin != nil {
			closeStdin()
		}
		var appErr *quic.ApplicationError
		if errors.As(acceptErr, &appErr) && appErr.ErrorCode == 0 {
			return nil
		}
		if LogDisconnect(util.LogPipe, acceptErr) {
			return nil
		}
		if errors.Is(acceptErr, context.Canceled) {
			// Local cancellation (e.g. SIGINT): normal shutdown, exit quietly.
			util.LogPipe.Printf("Accept inbound stream canceled")
			return nil
		}
		util.LogPipe.PrintfError("Accept inbound stream failed: %v", acceptErr)
		return acceptErr
	}

	inboundDone := make(chan struct{})
	go func() {
		defer close(inboundDone)
		buf := BufferPool.Get().([]byte)
		defer PutBuffer(buf)
		dst := io.Writer(tracker.RXWriter(os.Stdout))
		io.CopyBuffer(dst, s, buf)
	}()

	<-inboundDone
	if closeStdin != nil {
		// The peer finished sending (PROTOCOL.md §4: pipe exits when either
		// stream closes). Allow a half-close grace window for the local
		// stdin→stream copy to keep sending, then close the outbound stream
		// so the peer can finish too. If local stdin stays silent, the
		// detached copy goroutine may remain blocked on a read; it is cleaned
		// up on process exit.
		select {
		case <-stdinDone:
		case <-time.After(pipeCloseGrace):
		}
		closeStdin()
	}

	return nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
