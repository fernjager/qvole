package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRunExecCommand_Echo(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	execDone := make(chan error, 1)
	go func() {
		execDone <- RunExecCommand(context.Background(), serverConn, "echo hello-exec")
	}()

	stream, err := clientConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	var outBuf bytes.Buffer
	io.Copy(&outBuf, stream)

	err = <-execDone
	if err != nil {
		t.Fatalf("RunExecCommand: %v", err)
	}
	if !strings.Contains(outBuf.String(), "hello-exec") {
		t.Fatalf("expected 'hello-exec', got %q", outBuf.String())
	}
}

func TestRunExecCommand_ExitCode(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	execDone := make(chan error, 1)
	go func() {
		execDone <- RunExecCommand(context.Background(), serverConn, "sh -c 'exit 42'")
	}()

	stream, err := clientConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	io.ReadAll(stream)

	err = <-execDone
	if err == nil {
		t.Fatal("expected error for exit 42")
	}
	if !strings.Contains(err.Error(), "exit status 42") {
		t.Fatalf("expected exit status 42, got %v", err)
	}
}

func TestRunExecCommand_LargeOutput(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	execDone := make(chan error, 1)
	go func() {
		execDone <- RunExecCommand(context.Background(), serverConn, "sh -c 'dd if=/dev/zero bs=1048576 count=1 2>/dev/null'")
	}()

	stream, err := clientConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	var outBuf bytes.Buffer
	io.Copy(&outBuf, stream)

	err = <-execDone
	if err != nil {
		t.Fatalf("RunExecCommand: %v", err)
	}
	if outBuf.Len() != 1048576 {
		t.Fatalf("expected 1048576 bytes, got %d", outBuf.Len())
	}
}

func TestRunExecCommand_OpenStreamSyncError(t *testing.T) {
	_, serverConn := setupQUICPair(t)

	stream, err := serverConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	stream.Close()
	// Verify OpenStreamSync succeeds on a valid connection
}

func TestRunPipeMode_ContextCancel(t *testing.T) {
	_, serverConn := setupQUICPair(t)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- RunPipeMode(ctx, serverConn, "server")
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPipeMode did not exit after cancel")
	}
}

func TestRunPipeMode_ClosedStream(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	stream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	stream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- RunPipeMode(ctx, serverConn, "server")
	}()

	select {
	case err := <-done:
		// AcceptStream should succeed, bidirectionalCopy will finish when stream is closed
		if err != nil {
			t.Fatalf("RunPipeMode: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunPipeMode did not finish on closed stream")
	}
}

func TestRunExecCommand_NonexistentCommand(t *testing.T) {
	_, serverConn := setupQUICPair(t)

	execDone := make(chan error, 1)
	go func() {
		execDone <- RunExecCommand(context.Background(), serverConn, "nonexistent-binary-xyz-12345")
	}()

	// The command should fail quickly
	select {
	case err := <-execDone:
		if err == nil {
			t.Fatal("expected error for nonexistent command")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunExecCommand did not return for nonexistent command")
	}
}

func TestRunExecCommand_ContextCancellation(t *testing.T) {
	_, serverConn := setupQUICPair(t)

	ctx, cancel := context.WithCancel(context.Background())

	execDone := make(chan error, 1)
	go func() {
		execDone <- RunExecCommand(ctx, serverConn, "sleep 60")
	}()

	// Cancel after a brief delay
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-execDone:
		if err == nil {
			t.Fatal("expected error after context cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunExecCommand did not return after cancel")
	}
}

func TestRunExecCommand_StderrNotOnStream(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	execDone := make(chan error, 1)
	go func() {
		execDone <- RunExecCommand(context.Background(), serverConn, "sh -c 'echo stdout-msg; echo stderr-msg >&2'")
	}()

	stream, err := clientConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	var outBuf bytes.Buffer
	io.Copy(&outBuf, stream)

	<-execDone
	if !strings.Contains(outBuf.String(), "stdout-msg") {
		t.Fatalf("expected stdout output, got %q", outBuf.String())
	}
	if strings.Contains(outBuf.String(), "stderr-msg") {
		t.Fatal("stderr should not appear on stream")
	}
}

func TestSplitCommand_Empty(t *testing.T) {
	args := SplitCommand("")
	if len(args) != 0 {
		t.Fatalf("expected 0 args, got %d", len(args))
	}
}

func TestSplitCommand_Simple(t *testing.T) {
	args := SplitCommand("echo hello world")
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[0] != "echo" || args[1] != "hello" || args[2] != "world" {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestSplitCommand_SingleQuotes(t *testing.T) {
	args := SplitCommand("echo 'hello world' foo")
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "hello world" {
		t.Fatalf("expected 'hello world', got %q", args[1])
	}
}

func TestSplitCommand_DoubleQuotes(t *testing.T) {
	args := SplitCommand(`echo "hello world" foo`)
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "hello world" {
		t.Fatalf("expected 'hello world', got %q", args[1])
	}
}

func TestSplitCommand_UnmatchedSingleQuote(t *testing.T) {
	args := SplitCommand("echo 'unclosed")
	if args != nil {
		t.Fatalf("expected nil for unmatched quote, got %d: %v", len(args), args)
	}
}

func TestSplitCommand_UnmatchedDoubleQuote(t *testing.T) {
	args := SplitCommand(`echo "unclosed`)
	if args != nil {
		t.Fatalf("expected nil for unmatched quote, got %d: %v", len(args), args)
	}
}

func TestSplitCommand_MultipleSpaces(t *testing.T) {
	args := SplitCommand("echo   hello    world  ")
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
}

func TestSplitCommand_Tabs(t *testing.T) {
	args := SplitCommand("echo\thello\tworld")
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[0] != "echo" || args[1] != "hello" || args[2] != "world" {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestSplitCommand_NestedQuotes(t *testing.T) {
	args := SplitCommand(`echo "it's a test"`)
	if len(args) != 2 {
		t.Fatalf("expected 2 args, got %d: %v", len(args), args)
	}
	if args[1] != "it's a test" {
		t.Fatalf("expected \"it's a test\", got %q", args[1])
	}
}

func TestSplitCommand_JustWhitespace(t *testing.T) {
	args := SplitCommand("   \t  \t  ")
	if len(args) != 0 {
		t.Fatalf("expected 0 args for whitespace-only, got %d", len(args))
	}
}

func TestSplitCommand_SingleWord(t *testing.T) {
	args := SplitCommand("single")
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(args))
	}
	if args[0] != "single" {
		t.Fatalf("expected 'single', got %q", args[0])
	}
}

func TestRunExecCommand_EmptyCommand(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	execDone := make(chan error, 1)
	go func() {
		execDone <- RunExecCommand(context.Background(), clientConn, "")
	}()

	// Accept the stream on the server side
	stream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	stream.Close()

	select {
	case err := <-execDone:
		if err == nil {
			t.Fatal("expected error for empty command")
		}
		if !strings.Contains(err.Error(), "empty command") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunExecCommand did not return")
	}
}
