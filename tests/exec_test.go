package tests

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runExecPair(t *testing.T, relayAddr, code, command string) ([]byte, error) {
	t.Helper()
	logDir := t.TempDir()

	srvLog := filepath.Join(logDir, "srv_stderr.log")
	srvLogF, err := os.Create(srvLog)
	if err != nil {
		return nil, fmt.Errorf("create srv log: %w", err)
	}

	args := []string{"exec", "--cmd", command}
	args = append(args, clientModeArgs(relayAddr, "--code", code)...)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = strings.NewReader("")
	srvCmd.Stderr = srvLogF
	if err := srvCmd.Start(); err != nil {
		srvLogF.Close()
		return nil, fmt.Errorf("start server: %w", err)
	}
	defer srvCmd.Process.Kill()
	srvLogF.Close()

	cliStdoutR, cliStdoutW := mustPipe(t)

	cliLog := filepath.Join(logDir, "cli_stderr.log")
	cliLogF, err := os.Create(cliLog)
	if err != nil {
		return nil, fmt.Errorf("create cli log: %w", err)
	}

	cliArgs := append([]string{"exec"}, clientModeArgs(relayAddr, "--code", code)...)
	cliCmd := exec.Command(qvoleBin, cliArgs...)
	cliCmd.Stdin = strings.NewReader("")
	cliCmd.Stdout = cliStdoutW
	cliCmd.Stderr = cliLogF
	if err := cliCmd.Start(); err != nil {
		cliLogF.Close()
		return nil, fmt.Errorf("start client: %w", err)
	}
	defer cliCmd.Process.Kill()
	cliLogF.Close()

	done := make(chan struct{})
	var output bytes.Buffer
	go func() {
		buf := make([]byte, 65536)
		deadline := time.Now().Add(testTimeout)
		for {
			cliStdoutR.SetReadDeadline(time.Now().Add(testReadPoll))
			n, err := cliStdoutR.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
			}
			if err != nil {
				if isTimeout(err) {
					if time.Now().After(deadline) {
						break
					}
					continue
				}
				break
			}
			if time.Now().After(deadline) {
				break
			}
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		srvLogData, _ := os.ReadFile(srvLog)
		cliLogData, _ := os.ReadFile(cliLog)
		return nil, fmt.Errorf("client timed out after 30s\nSRV: %s\nCLI: %s",
			string(srvLogData), string(cliLogData))
	}

	trimmed := bytes.TrimSpace(output.Bytes())
	if len(trimmed) == 0 {
		srvLogData, _ := os.ReadFile(srvLog)
		cliLogData, _ := os.ReadFile(cliLog)
		return nil, fmt.Errorf("empty output\nSRV: %s\nCLI: %s",
			string(srvLogData), string(cliLogData))
	}

	return trimmed, nil
}

func TestExecBasic(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9904-alpha-bravo-charlie"

	out, err := runExecPair(t, relayAddr, code, "echo exec-basic-payload")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("output: %s", string(out))
}

func TestExecEnvvar(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	secret := "9914-exec-env-secret-test"
	logDir := t.TempDir()

	srvLog := filepath.Join(logDir, "srv_stderr.log")
	srvLogF, _ := os.Create(srvLog)

	args := []string{"exec", "--cmd", "echo exec-env-test"}
	args = append(args, clientModeArgs(relayAddr)...)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Env = append(os.Environ(), "QVOLE_CODE="+secret)
	srvCmd.Stdin = strings.NewReader("")
	srvCmd.Stderr = srvLogF
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()
	srvLogF.Close()

	var output string
	for attempt := 0; attempt < 3; attempt++ {
		cliStdoutR, cliStdoutW := mustPipe(t)

		cliLog := filepath.Join(logDir, fmt.Sprintf("cli_%d.log", attempt))
		cliLogF, _ := os.Create(cliLog)

		cliArgs := append([]string{"exec"}, clientModeArgs(relayAddr)...)
		cliCmd := exec.Command(qvoleBin, cliArgs...)
		cliCmd.Env = append(os.Environ(), "QVOLE_CODE="+secret)
		cliCmd.Stdin = strings.NewReader("")
		cliCmd.Stdout = cliStdoutW
		cliCmd.Stderr = cliLogF
		if err := cliCmd.Start(); err != nil {
			cliLogF.Close()
			continue
		}

		done := make(chan struct{})
		var out bytes.Buffer
		go func() {
			buf := make([]byte, 65536)
			deadline := time.Now().Add(10 * time.Second)
			for {
				cliStdoutR.SetReadDeadline(time.Now().Add(testReadPoll))
				n, err := cliStdoutR.Read(buf)
				if n > 0 {
					out.Write(buf[:n])
				}
				if err != nil {
					if isTimeout(err) && time.Now().Before(deadline) {
						continue
					}
					break
				}
			}
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(15 * time.Second):
		}
		cliCmd.Process.Kill()
		cliLogF.Close()

		if out.Len() > 0 {
			output = out.String()
			break
		}
		if attempt < 2 {
			time.Sleep(1 * time.Second)
		}
	}

	if output == "" {
		data, _ := os.ReadFile(srvLog)
		t.Fatalf("client did not receive output in 5 attempts\nsrv log: %s", string(data))
	}
	t.Logf("output: %s", strings.TrimSpace(output))
}

func TestExecExitCode(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9916-exec-exit-code-test"
	logDir := t.TempDir()

	srvLog := filepath.Join(logDir, "srv_stderr.log")
	srvLogF, _ := os.Create(srvLog)

	args := []string{"exec", "--cmd", "sh -c 'exit 42'"}
	args = append(args, clientModeArgs(relayAddr, "--code", code)...)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = strings.NewReader("")
	srvCmd.Stderr = srvLogF
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()
	srvLogF.Close()

	cliDone := make(chan struct{})
	go func() {
		cliArgs := append([]string{"exec"}, clientModeArgs(relayAddr, "--code", code)...)
		cliCmd := exec.Command(qvoleBin, cliArgs...)
		cliCmd.Stdin = strings.NewReader("")
		cliCmd.Stderr = &logWriter{t: t, prefix: "[cli-stderr] "}
		cliCmd.Run()
		close(cliDone)
	}()

	err := srvCmd.Wait()
	select {
	case <-cliDone:
	case <-time.After(5 * time.Second):
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 42 {
				return
			}
			t.Fatalf("expected exit code 42, got %d", exitErr.ExitCode())
		}
		data, _ := os.ReadFile(srvLog)
		t.Fatalf("unexpected error: %v\nsrv log: %s", err, string(data))
	}
	t.Fatal("expected error (exit code 42) but server exited successfully")
}

func TestExecLargeOutput(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9917-exec-large-output-test"

	out, err := runExecPair(t, relayAddr, code,
		"sh -c 'dd if=/dev/urandom bs=1024 count=128 2>/dev/null | base64'")
	if err != nil {
		t.Fatal(err)
	}
	size := len(out)
	t.Logf("output size: %d bytes", size)
	if size < 100000 {
		t.Errorf("output too small: %d bytes (expected > 100000)", size)
	}
}

func TestWrongCodeExec(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	codeA := "9926-alpha-bravo-charlie"
	codeB := "9926-delta-echo-foxtrot"
	logDir := t.TempDir()

	srvLog := filepath.Join(logDir, "srv_stderr.log")
	srvLogF, _ := os.Create(srvLog)

	args := []string{"exec", "--cmd", "echo test"}
	args = append(args, clientModeArgs(relayAddr, "--code", codeA)...)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = strings.NewReader("")
	srvCmd.Stderr = srvLogF
	srvCmd.Env = append(os.Environ(), "QVOLE_EXCHANGE_DEADLINE_MS=5000")
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()
	srvLogF.Close()

	cliLog := filepath.Join(logDir, "cli_stderr.log")
	cliLogF, _ := os.Create(cliLog)

	cliArgs := append([]string{"exec"}, clientModeArgs(relayAddr, "--code", codeB)...)
	cliCmd := exec.Command(qvoleBin, cliArgs...)
	cliCmd.Stdin = strings.NewReader("")
	cliCmd.Stderr = cliLogF
	cliCmd.Env = append(os.Environ(), "QVOLE_EXCHANGE_DEADLINE_MS=5000")
	if err := cliCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	defer cliCmd.Process.Kill()
	cliLogF.Close()

	waitForExit(srvCmd, 15*time.Second)
	waitForExit(cliCmd, 15*time.Second)
	srvCmd.Process.Kill()
	cliCmd.Process.Kill()

	srvData, _ := os.ReadFile(srvLog)
	cliData, _ := os.ReadFile(cliLog)

	if bytes.Contains(srvData, []byte("timeout exchanging")) ||
		bytes.Contains(cliData, []byte("timeout exchanging")) {
		return
	}
	t.Errorf("neither peer reported timeout exchanging\nSRV: %s\nCLI: %s",
		string(srvData), string(cliData))
}

func TestExecNonexistent(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9927-exec-nonexistent-test"
	logDir := t.TempDir()

	srvLog := filepath.Join(logDir, "srv_stderr.log")
	srvLogF, _ := os.Create(srvLog)

	args := []string{"exec", "--cmd", "nonexistent-binary-xyz-12345"}
	args = append(args, clientModeArgs(relayAddr, "--code", code)...)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = strings.NewReader("")
	srvCmd.Stderr = srvLogF
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()
	srvLogF.Close()

	cliLog := filepath.Join(logDir, "cli_stderr.log")
	cliLogF, _ := os.Create(cliLog)

	cliArgs := append([]string{"exec"}, clientModeArgs(relayAddr, "--code", code)...)
	cliCmd := exec.Command(qvoleBin, cliArgs...)
	cliCmd.Stdin = strings.NewReader("")
	cliCmd.Stderr = cliLogF
	cliCmd.Run()
	cliLogF.Close()

	srvCmd.Wait()

	srvData, _ := os.ReadFile(srvLog)
	cliData, _ := os.ReadFile(cliLog)
	if bytes.Contains(bytes.ToLower(srvData), []byte("error")) ||
		bytes.Contains(bytes.ToLower(srvData), []byte("not found")) ||
		bytes.Contains(bytes.ToLower(srvData), []byte("no such")) {
		return
	}
	t.Errorf("server did not report error for nonexistent command\nSRV: %s\nCLI: %s",
		string(srvData), string(cliData))
}
