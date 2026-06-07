package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
)

const execDrainTimeout = 5 * time.Second

// RunExec connects to a peer and either runs a command locally or bridges stdin/stdout,
// depending on cmdMode. When cmdMode is true, the local command's stdin/stdout/stderr
// are bridged over a QUIC stream; exit code is propagated.
func RunExec(ctx context.Context, relayAddr, code, command string, cmdMode bool) error {
	conn, isServer, err := engine.ConnectPeer(ctx, relayAddr, code)
	if err != nil {
		return err
	}

	role := util.RoleString(isServer)

	if cmdMode {
		util.LogExec.PrintfSuccess("Connected as %s", role)
		util.LogExec.PrintfInfo("Running command: %s", util.Bold(command))
		err := RunExecCommand(ctx, conn, command)
		conn.CloseWithError(0, "")
		return err
	}
	defer conn.CloseWithError(0, "done")
	return RunPipeMode(ctx, conn, role)
}

// RunExecCommand opens a QUIC stream, runs the given command with stdin/stdout bridged
// to the stream, and returns the command's exit error.
// When the peer disconnects (stream EOF), the child process is killed via context cancellation.
func RunExecCommand(ctx context.Context, conn *quic.Conn, command string) error {
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()

	args := SplitCommand(command)
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}

	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	cmd := exec.CommandContext(execCtx, args[0], args[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = stream
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return err
	}

	copyDone := make(chan struct{})
	go func() {
		io.Copy(stdin, stream)
		stdin.Close()
		execCancel()
		close(copyDone)
	}()

	cmdErr := cmd.Wait()
	if cmdErr != nil {
		util.LogExec.PrintfInfo("Command exited: %v", cmdErr)
	}
	execDrainTimeoutVal := util.EnvDuration("QVOLE_EXEC_DRAIN_TIMEOUT_MS", execDrainTimeout)
	select {
	case <-copyDone:
	case <-execCtx.Done():
	case <-time.After(execDrainTimeoutVal):
	}
	return cmdErr
}

// RunPipeMode accepts an inbound QUIC stream and bridges it to stdin/stdout.
func RunPipeMode(ctx context.Context, conn *quic.Conn, role string) error {
	util.LogExec.PrintfSuccess("Connected as %s", role)

	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		var appErr *quic.ApplicationError
		if errors.As(err, &appErr) && appErr.ErrorCode == 0 {
			return nil
		}
		return err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		StartStdinPipe(ctx, stream)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		stream.Close()
		return ctx.Err()
	}
}

// SplitCommand splits a command string into arguments using minimal shell-like parsing.
// Only handles single quotes, double quotes, and space/tab separation.
// Does not handle escape sequences, backtick substitution, glob expansion, redirection,
// or pipeline operators. May return nil for unmatched quotes.
func SplitCommand(s string) []string {
	var args []string
	var current []byte
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inSingle {
			if c == '\'' {
				inSingle = false
			} else {
				current = append(current, c)
			}
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			} else {
				current = append(current, c)
			}
			continue
		}
		if c == '\'' {
			inSingle = true
			continue
		}
		if c == '"' {
			inDouble = true
			continue
		}
		if c == ' ' || c == '\t' {
			if len(current) > 0 {
				args = append(args, string(current))
				current = nil
			}
			continue
		}
		current = append(current, c)
	}
	if inSingle || inDouble {
		return nil
	} else if len(current) > 0 {
		args = append(args, string(current))
	}
	return args
}
