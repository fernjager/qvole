//go:build !windows

package main

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func runForExitErr(t *testing.T, cmd *exec.Cmd) *exec.ExitError {
	t.Helper()
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %v", err)
	}
	return exitErr
}

func TestChildExitStatus_ExitCode(t *testing.T) {
	exitErr := runForExitErr(t, exec.Command("sh", "-c", "exit 42"))
	if code := childExitStatus(context.Background(), exitErr); code != 42 {
		t.Fatalf("childExitStatus = %d, want 42", code)
	}
}

func TestChildExitStatus_KilledByInterrupt(t *testing.T) {
	// SIGINT cancels ctx; CommandContext kills the child (ExitCode -1).
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	cancel()
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() >= 0 {
		t.Fatalf("expected signal-killed child (ExitCode -1), got %v", err)
	}
	if code := childExitStatus(ctx, exitErr); code != exitCodeInterrupt {
		t.Fatalf("childExitStatus = %d, want %d", code, exitCodeInterrupt)
	}
}

func TestChildExitStatus_KilledByPeerDisconnect(t *testing.T) {
	// The peer closing the stream kills the child while our own signal
	// context is still live: a clean end, not an interrupt.
	killCtx, kill := context.WithCancel(context.Background())
	defer kill()
	cmd := exec.CommandContext(killCtx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	kill()
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() >= 0 {
		t.Fatalf("expected signal-killed child (ExitCode -1), got %v", err)
	}
	if code := childExitStatus(context.Background(), exitErr); code != 0 {
		t.Fatalf("childExitStatus = %d, want 0 (clean peer-driven end)", code)
	}
}
