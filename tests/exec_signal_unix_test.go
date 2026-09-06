//go:build !windows

package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestExecSIGINTExitCode verifies that interrupting `exec --cmd` reports the
// conventional 130 (128+SIGINT) instead of 255: the context cancellation
// kills the child via exec.CommandContext, whose Wait error carries
// ExitCode() == -1.
func TestExecSIGINTExitCode(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9930-exec-sigint-exit"
	logDir := t.TempDir()

	srvLog := filepath.Join(logDir, "srv_stderr.log")
	srvLogF, err := os.Create(srvLog)
	if err != nil {
		t.Fatalf("create srv log: %v", err)
	}
	defer srvLogF.Close()

	args := []string{"exec", "--cmd", "sleep 60"}
	args = append(args, clientModeArgs(relayAddr, "--code", code)...)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = strings.NewReader("")
	srvCmd.Stderr = srvLogF
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	// The client's stdin must stay open, or its EOF ends the session before
	// the signal lands.
	cliStdin, closeStdin := blockingStdin()
	defer closeStdin()
	cliArgs := append([]string{"exec"}, clientModeArgs(relayAddr, "--code", code)...)
	cliCmd := exec.Command(qvoleBin, cliArgs...)
	cliCmd.Stdin = cliStdin
	cliCmd.Stderr = &logWriter{t: t, prefix: "[cli-stderr] "}
	if err := cliCmd.Start(); err != nil {
		srvCmd.Process.Kill()
		t.Fatalf("start client: %v", err)
	}
	defer cliCmd.Process.Kill()

	// Wait until the server is actually running the command, so the signal
	// hits the exec --cmd path rather than the connection setup.
	deadline := time.Now().Add(testTimeout)
	for {
		data, _ := os.ReadFile(srvLog)
		if strings.Contains(string(data), "Running command") {
			break
		}
		if time.Now().After(deadline) {
			srvCmd.Process.Kill()
			data, _ := os.ReadFile(srvLog)
			t.Fatalf("server did not start the command\nSRV: %s", string(data))
		}
		time.Sleep(200 * time.Millisecond)
	}

	if err := srvCmd.Process.Signal(syscall.SIGINT); err != nil {
		srvCmd.Process.Kill()
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- srvCmd.Wait() }()
	select {
	case err := <-done:
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			data, _ := os.ReadFile(srvLog)
			t.Fatalf("expected exit error, got %v\nSRV: %s", err, string(data))
		}
		if exitErr.ExitCode() != 130 {
			data, _ := os.ReadFile(srvLog)
			t.Fatalf("exit code = %d, want 130\nSRV: %s", exitErr.ExitCode(), string(data))
		}
	case <-time.After(10 * time.Second):
		srvCmd.Process.Kill()
		t.Fatal("server did not exit within 10s after SIGINT")
	}
	fmt.Println("server exited 130 on SIGINT")
}
