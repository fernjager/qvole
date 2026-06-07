package tests

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBasicPipe(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	payload := []byte("hello-basic-001\n")
	code := "9900-pipe-basic-test"

	srvStdinR, srvStdinW := mustPipe(t)
	cliStdoutR, cliStdoutW := mustPipe(t)

	args := pipeArgs(relayAddr, code)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = srvStdinR
	srvCmd.Stderr = &logWriter{t: t, prefix: "[srv-stderr] "}
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()

	if _, err := srvStdinW.Write(payload); err != nil {
		t.Fatalf("write server stdin: %v", err)
	}
	srvStdinW.Close()

	cliStdinR, cliStdinW := mustPipe(t)
	args = pipeArgs(relayAddr, code)
	cliCmd := exec.Command(qvoleBin, args...)
	cliCmd.Stdin = cliStdinR
	cliCmd.Stdout = cliStdoutW
	cliCmd.Stderr = &logWriter{t: t, prefix: "[cli-stderr] "}
	if err := cliCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	defer cliCmd.Process.Kill()

	ok, got := readUntilContains(cliStdoutR, payload, testTimeout)
	cliStdinW.Close()
	if !ok {
		t.Errorf("client did not receive payload (got %q)", got)
	}
}

func TestExplicitCode(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	payload := "explicit-002"
	code := "9901-ability-subway-unicorn"

	srvStdinR, srvStdinW := mustPipe(t)

	args := pipeArgs(relayAddr, code)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = srvStdinR
	srvCmd.Stderr = &logWriter{t: t, prefix: "[srv-stderr] "}
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()

	if _, err := srvStdinW.Write(append([]byte(payload), '\n')); err != nil {
		t.Fatalf("write server stdin: %v", err)
	}
	srvStdinW.Close()

	cliStdinR, cliStdinW := mustPipe(t)
	cliStdoutR, cliStdoutW := mustPipe(t)

	args = pipeArgs(relayAddr, code)
	cliCmd := exec.Command(qvoleBin, args...)
	cliCmd.Stdin = cliStdinR
	cliCmd.Stdout = cliStdoutW
	cliCmd.Stderr = &logWriter{t: t, prefix: "[cli-stderr] "}
	if err := cliCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	defer cliCmd.Process.Kill()

	ok, got := readUntilContains(cliStdoutR, []byte(payload), testTimeout)
	cliStdinW.Close()
	if !ok {
		t.Errorf("client did not receive payload (got %q)", got)
	}
}

func TestWrongCode(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	codeA := "9902-alpha-bravo-charlie"
	codeB := "9902-delta-echo-foxtrot"
	logDir := t.TempDir()

	logA := filepath.Join(logDir, "a_stderr.log")
	logB := filepath.Join(logDir, "b_stderr.log")
	logAF, _ := os.Create(logA)
	logBF, _ := os.Create(logB)

	stdinA, closeA := blockingStdin()
	defer closeA()
	stdinB, closeB := blockingStdin()
	defer closeB()

	argsA := pipeArgs(relayAddr, codeA)
	cmdA := exec.Command(qvoleBin, argsA...)
	cmdA.Stdin = stdinA
	cmdA.Stderr = logAF
	cmdA.Env = append(os.Environ(), "QVOLE_EXCHANGE_DEADLINE_MS=5000")
	if err := cmdA.Start(); err != nil {
		t.Fatalf("start A: %v", err)
	}
	logAF.Close()

	argsB := pipeArgs(relayAddr, codeB)
	cmdB := exec.Command(qvoleBin, argsB...)
	cmdB.Stdin = stdinB
	cmdB.Stderr = logBF
	cmdB.Env = append(os.Environ(), "QVOLE_EXCHANGE_DEADLINE_MS=5000")
	if err := cmdB.Start(); err != nil {
		t.Fatalf("start B: %v", err)
	}
	logBF.Close()

	done := make(chan struct{}, 2)
	go func() { cmdA.Wait(); done <- struct{}{} }()
	go func() { cmdB.Wait(); done <- struct{}{} }()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		cmdA.Process.Kill()
		cmdB.Process.Kill()
		<-done
		<-done
	}

	dataA, _ := os.ReadFile(logA)
	dataB, _ := os.ReadFile(logB)

	if bytes.Contains(dataA, []byte("timeout exchanging")) ||
		bytes.Contains(dataB, []byte("timeout exchanging")) {
		return
	}

	t.Errorf("neither peer reported timeout exchanging\nA: %s\nB: %s", string(dataA), string(dataB))
}

func TestBidiPipe(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	srvPayload := "server-to-client"
	cliPayload := "client-to-server"
	logDir := t.TempDir()

	srvStdinR, srvStdinW := mustPipe(t)
	srvStdoutR, srvStdoutW := mustPipe(t)
	cliStdoutR, cliStdoutW := mustPipe(t)
	cliStdinR, cliStdinW := mustPipe(t)

	srvLog := filepath.Join(logDir, "srv_stderr.log")
	srvLogF, _ := os.Create(srvLog)

	args := pipeArgs(relayAddr)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = srvStdinR
	srvCmd.Stdout = srvStdoutW
	srvCmd.Stderr = srvLogF
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()
	srvLogF.Close()

	code := captureCode(t, srvLog, 15*time.Second)
	if code == "" {
		t.Fatal("failed to capture code from server log")
	}
	t.Logf("code: %s", code)

	args = pipeArgs(relayAddr, code)
	cliCmd := exec.Command(qvoleBin, args...)
	cliCmd.Stdin = cliStdinR
	cliCmd.Stdout = cliStdoutW
	cliCmd.Stderr = &logWriter{t: t, prefix: "[cli-stderr] "}
	if err := cliCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	defer cliCmd.Process.Kill()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		srvStdinW.Write(append([]byte(srvPayload), '\n'))
		srvStdinW.Close()
	}()

	go func() {
		defer wg.Done()
		time.Sleep(1 * time.Second)
		cliStdinW.Write(append([]byte(cliPayload), '\n'))
		cliStdinW.Close()
	}()

	var readWg sync.WaitGroup
	var srvOk, cliOk bool
	var srvGot, cliGot string
	readWg.Add(2)

	go func() {
		defer readWg.Done()
		srvOk, srvGot = readUntilContains(srvStdoutR, []byte(cliPayload), testTimeout)
	}()

	go func() {
		defer readWg.Done()
		cliOk, cliGot = readUntilContains(cliStdoutR, []byte(srvPayload), testTimeout)
	}()

	wg.Wait()
	readWg.Wait()

	if !cliOk {
		t.Errorf("client did not receive server payload (got %q)", cliGot)
	}
	if !srvOk {
		t.Errorf("server did not receive client payload (got %q)", srvGot)
	}
}

func TestLargePayload(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	payloadSize := 65536

	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	srvStdinR, srvStdinW := mustPipe(t)
	cliStdoutR, cliStdoutW := mustPipe(t)
	cliStdinR, _ := mustPipe(t)

	srvLog := filepath.Join(t.TempDir(), "srv_stderr.log")
	srvLogF, _ := os.Create(srvLog)

	args := pipeArgs(relayAddr)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = srvStdinR
	srvCmd.Stderr = srvLogF
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()
	srvLogF.Close()

	if _, err := srvStdinW.Write(payload); err != nil {
		t.Fatalf("write server stdin: %v", err)
	}
	srvStdinW.Close()

	code := captureCode(t, srvLog, 15*time.Second)
	if code == "" {
		t.Fatal("failed to capture code from server log")
	}
	t.Logf("code: %s", code)

	args = pipeArgs(relayAddr, code)
	cliCmd := exec.Command(qvoleBin, args...)
	cliCmd.Stdin = cliStdinR
	cliCmd.Stdout = cliStdoutW
	cliCmd.Stderr = &logWriter{t: t, prefix: "[cli-stderr] "}
	if err := cliCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	defer cliCmd.Process.Kill()

	var received bytes.Buffer
	buf := make([]byte, 65536)
	deadline := time.Now().Add(testTimeout)
	for {
		cliStdoutR.SetReadDeadline(time.Now().Add(testReadPoll))
		n, err := cliStdoutR.Read(buf)
		if n > 0 {
			received.Write(buf[:n])
			if received.Len() >= payloadSize {
				if bytes.Equal(received.Bytes()[:payloadSize], payload) {
					return
				}
				t.Errorf("payload mismatch after %d bytes", received.Len())
				return
			}
		}
		if err != nil {
			if isTimeout(err) {
				if time.Now().After(deadline) {
					t.Errorf("timeout: got %d of %d bytes", received.Len(), payloadSize)
					return
				}
				continue
			}
			t.Errorf("read: %v (got %d of %d bytes)", err, received.Len(), payloadSize)
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("timeout: got %d of %d bytes", received.Len(), payloadSize)
			return
		}
	}
}

func TestPipeEnvvar(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	secret := "9915-pipe-env-secret-test"
	payload := "pipe-env-payload"

	srvStdinR, srvStdinW := mustPipe(t)

	args := pipeArgs(relayAddr)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Env = append(os.Environ(), "QVOLE_CODE="+secret)
	srvCmd.Stdin = srvStdinR
	srvCmd.Stderr = &logWriter{t: t, prefix: "[srv-stderr] "}
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()

	if _, err := srvStdinW.Write(append([]byte(payload), '\n')); err != nil {
		t.Fatalf("write server stdin: %v", err)
	}
	srvStdinW.Close()

	cliStdinR, cliStdinW := mustPipe(t)
	cliStdoutR, cliStdoutW := mustPipe(t)

	args = pipeArgs(relayAddr)
	cliCmd := exec.Command(qvoleBin, args...)
	cliCmd.Env = append(os.Environ(), "QVOLE_CODE="+secret)
	cliCmd.Stdin = cliStdinR
	cliCmd.Stdout = cliStdoutW
	cliCmd.Stderr = &logWriter{t: t, prefix: "[cli-stderr] "}
	if err := cliCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	defer cliCmd.Process.Kill()

	ok, got := readUntilContains(cliStdoutR, []byte(payload), testTimeout)
	cliStdinW.Close()
	if !ok {
		t.Errorf("client did not receive payload (got %q)", got)
	}
}

func TestMultiClient(t *testing.T) {
	relayAddrA, cleanupA := testRelay(t)
	defer cleanupA()
	relayAddrB, cleanupB := testRelay(t)
	defer cleanupB()

	payloadA := "multi-a-payload"
	payloadB := "multi-b-payload"
	codeA := "9903-alpha-bravo-charlie"
	codeB := "9904-delta-echo-foxtrot"

	runPair := func(t *testing.T, relay, code, payload, label string) bool {
		t.Helper()
		srvStdinR, srvStdinW, err := os.Pipe()
		if err != nil {
			t.Logf("%s: srv stdin pipe: %v", label, err)
			return false
		}
		cliStdoutR, cliStdoutW, err := os.Pipe()
		if err != nil {
			t.Logf("%s: cli stdout pipe: %v", label, err)
			return false
		}
		cliStdinR, cliStdinW := mustPipe(t)

		srvLog, _ := os.Create(t.TempDir() + "/" + label + "_srv.log")
		args := pipeArgs(relay, code)
		srvCmd := exec.Command(qvoleBin, args...)
		srvCmd.Stdin = srvStdinR
		srvCmd.Stderr = srvLog
		if err := srvCmd.Start(); err != nil {
			t.Logf("%s: start server: %v", label, err)
			srvLog.Close()
			return false
		}
		defer srvCmd.Process.Kill()
		srvLog.Close()

		srvStdinW.Write(append([]byte(payload), '\n'))
		srvStdinW.Close()

		time.Sleep(1 * time.Second)

		cliLog, _ := os.Create(t.TempDir() + "/" + label + "_cli.log")
		args = pipeArgs(relay, code)
		cliCmd := exec.Command(qvoleBin, args...)
		cliCmd.Stdin = cliStdinR
		cliCmd.Stdout = cliStdoutW
		cliCmd.Stderr = cliLog
		if err := cliCmd.Start(); err != nil {
			t.Logf("%s: start client: %v", label, err)
			cliLog.Close()
			return false
		}
		defer cliCmd.Process.Kill()
		cliLog.Close()

		ok, got := readUntilContains(cliStdoutR, []byte(payload), testTimeout)
		cliStdinW.Close()
		if !ok {
			t.Logf("%s: payload not received (got %q)", label, got)
		}
		return ok
	}

	if !runPair(t, relayAddrA, codeA, payloadA, "A") {
		t.Error("pair A failed")
	}
	if !runPair(t, relayAddrB, codeB, payloadB, "B") {
		t.Error("pair B failed")
	}
}

func TestDisconnect(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9938-disconnect-test"
	payload := "disconnect-payload"

	srvStdinR, srvStdinW := mustPipe(t)

	args := pipeArgs(relayAddr, code)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = srvStdinR
	srvCmd.Stderr = &logWriter{t: t, prefix: "[srv-stderr] "}
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}

	cliStdinR, cliStdinW := mustPipe(t)
	args = pipeArgs(relayAddr, code)
	cliCmd := exec.Command(qvoleBin, args...)
	cliCmd.Stdin = cliStdinR
	cliCmd.Stderr = &logWriter{t: t, prefix: "[cli-stderr] "}
	cliStdout := new(bytes.Buffer)
	cliCmd.Stdout = cliStdout
	if err := cliCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}

	time.Sleep(2 * time.Second)

	srvStdinW.Write(append([]byte(payload), '\n'))
	srvStdinW.Close()
	time.Sleep(1 * time.Second)

	srvCmd.Process.Kill()
	srvCmd.Wait()

	cliStdinW.Close()

	if !waitForExit(cliCmd, 10*time.Second) {
		t.Fatal("client did not exit after server disconnect")
	}

	if !bytes.Contains(cliStdout.Bytes(), []byte(payload)) {
		t.Errorf("client did not receive payload (got %q)", cliStdout.String())
	}
}

func TestPipeStats(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9940-stats-pipe-test"
	payload := []byte("stats-test-payload\n")

	srvStdinR, srvStdinW := mustPipe(t)
	cliStdoutR, cliStdoutW := mustPipe(t)
	cliStdinR, cliStdinW := mustPipe(t)

	srvLog := filepath.Join(t.TempDir(), "srv_stderr.log")
	srvLogF, _ := os.Create(srvLog)

	args := pipeArgs(relayAddr, code)
	srvCmd := exec.Command(qvoleBin, args...)
	srvCmd.Stdin = srvStdinR
	srvCmd.Stderr = srvLogF
	if err := srvCmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer srvCmd.Process.Kill()
	srvLogF.Close()

	if _, err := srvStdinW.Write(payload); err != nil {
		t.Fatalf("write server stdin: %v", err)
	}
	srvStdinW.Close()

	var cliStderr bytes.Buffer
	args = pipeArgs(relayAddr, code)
	cliCmd := exec.Command(qvoleBin, args...)
	cliCmd.Stdin = cliStdinR
	cliCmd.Stdout = cliStdoutW
	cliCmd.Stderr = &cliStderr
	if err := cliCmd.Start(); err != nil {
		t.Fatalf("start client: %v", err)
	}
	defer cliCmd.Process.Kill()

	ok, got := readUntilContains(cliStdoutR, payload, testTimeout)
	cliStdinW.Close()
	if !ok {
		t.Errorf("client did not receive payload (got %q)", got)
	}

	srvCmd.Process.Kill()
	srvCmd.Wait()
	cliCmd.Process.Kill()
	cliCmd.Wait()

	srvData, _ := os.ReadFile(srvLog)
	cliData := cliStderr.String()

	if !bytes.Contains(srvData, []byte("Total:")) && !bytes.Contains([]byte(cliData), []byte("Total:")) {
		t.Errorf("neither peer logged stats totals\nSRV: %s\nCLI: %s", string(srvData), cliData)
	}
}
