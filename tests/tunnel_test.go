package tests

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func runTunnelRemote(t *testing.T, echoPort, tunnelPort int, payload string, code string) {
	t.Helper()
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	echoAddr := fmt.Sprintf("127.0.0.1:%d", echoPort)
	tunnelAddr := fmt.Sprintf("127.0.0.1:%d", tunnelPort)

	_, echoClose := startEchoServer(t, echoAddr)
	defer echoClose()
	waitForListener(t, echoAddr, 5*time.Second)

	logDir := t.TempDir()
	initLog := filepath.Join(logDir, "init_stderr.log")
	initLogF, _ := os.Create(initLog)

	args := []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code, "--allow-tunnel")...)
	initCmd := exec.Command(qvoleBin, args...)
	initCmd.Stderr = initLogF
	if err := initCmd.Start(); err != nil {
		t.Fatalf("start init: %v", err)
	}
	defer initCmd.Process.Kill()
	initLogF.Close()

	peerLog := filepath.Join(logDir, "peer_stderr.log")
	peerLogF, _ := os.Create(peerLog)

	args = []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code,
		"-R", fmt.Sprintf("%s:127.0.0.1:%d", tunnelAddr, echoPort))...)
	peerCmd := exec.Command(qvoleBin, args...)
	peerCmd.Stderr = peerLogF
	if err := peerCmd.Start(); err != nil {
		t.Fatalf("start peer: %v", err)
	}
	defer peerCmd.Process.Kill()
	peerLogF.Close()

	waitForListener(t, tunnelAddr, 15*time.Second)

	conn, err := net.Dial("tcp", tunnelAddr)
	if err != nil {
		initData, _ := os.ReadFile(initLog)
		peerData, _ := os.ReadFile(peerLog)
		t.Fatalf("%v\nINIT: %s\nPEER: %s", err, string(initData), string(peerData))
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 4096)
	readDeadline := time.Now().Add(10 * time.Second)
	for {
		conn.SetReadDeadline(time.Now().Add(testReadPoll))
		n, readErr := conn.Read(buf)
		if n > 0 {
			if bytes.Contains(buf[:n], []byte(payload)) {
				return
			}
		}
		if readErr != nil {
			if isTimeout(readErr) {
				if time.Now().After(readDeadline) {
					t.Fatalf("read: timeout (got partial %q)", string(buf[:n]))
				}
				continue
			}
			t.Fatalf("read: %v (got %q)", readErr, string(buf[:n]))
		}
		if time.Now().After(readDeadline) {
			t.Fatalf("read: timeout (got partial %q)", string(buf[:n]))
		}
	}
}

func TestTunnelBasic(t *testing.T) {
	runTunnelRemote(t, 19008, 19999, "hello-forward-test", "9905-alpha-bravo-charlie")
}

func TestTunnelRemote(t *testing.T) {
	runTunnelRemote(t, 19013, 19998, "hello-remote-forward", "9909-alpha-bravo-charlie")
}

func TestTunnelMulti(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	echoPort1 := 19019
	echoPort2 := 19020
	fwdPort := 19021
	code := "9918-alpha-bravo-charlie"

	_, echoClose1 := startEchoServer(t, fmt.Sprintf("127.0.0.1:%d", echoPort1))
	defer echoClose1()
	_, echoClose2 := startEchoServer(t, fmt.Sprintf("127.0.0.1:%d", echoPort2))
	defer echoClose2()
	waitForListener(t, fmt.Sprintf("127.0.0.1:%d", echoPort1), 5*time.Second)
	waitForListener(t, fmt.Sprintf("127.0.0.1:%d", echoPort2), 5*time.Second)

	logDir := t.TempDir()

	peerLog := filepath.Join(logDir, "peer_stderr.log")
	peerLogF, _ := os.Create(peerLog)
	args := []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code, "--allow-tunnel")...)
	peerCmd := exec.Command(qvoleBin, args...)
	peerCmd.Stderr = peerLogF
	if err := peerCmd.Start(); err != nil {
		t.Fatalf("start peer: %v", err)
	}
	defer peerCmd.Process.Kill()
	peerLogF.Close()
	time.Sleep(1 * time.Second)

	fwdPort2 := fwdPort + 1
	initLog := filepath.Join(logDir, "init_stderr.log")
	initLogF, _ := os.Create(initLog)
	args = []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code,
		"-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", fwdPort, echoPort1),
		"-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", fwdPort2, echoPort2))...)
	initCmd := exec.Command(qvoleBin, args...)
	initCmd.Stderr = initLogF
	if err := initCmd.Start(); err != nil {
		t.Fatalf("start init: %v", err)
	}
	defer initCmd.Process.Kill()
	initLogF.Close()

	fwdAddr1 := fmt.Sprintf("127.0.0.1:%d", fwdPort)
	fwdAddr2 := fmt.Sprintf("127.0.0.1:%d", fwdPort2)
	waitForListener(t, fwdAddr1, 15*time.Second)
	waitForListener(t, fwdAddr2, 15*time.Second)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	testPort := func(port int, label string) {
		defer wg.Done()
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			errCh <- fmt.Errorf("%s: %w", label, err)
			return
		}
		defer conn.Close()

		payload := []byte("test-" + label)
		conn.Write(payload)

		buf := make([]byte, 4096)
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, readErr := conn.Read(buf)
		if n > 0 && bytes.Contains(buf[:n], payload) {
			return
		}
		errCh <- fmt.Errorf("%s: read failed: %v (got %d bytes)", label, readErr, n)
	}

	wg.Add(2)
	go testPort(fwdPort, "port1")
	go testPort(fwdPort2, "port2")
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

func TestTunnelLocal(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	echoPort := 19024
	fwdPort := 19025
	code := "9922-alpha-bravo-charlie"

	_, echoClose := startEchoServer(t, fmt.Sprintf("127.0.0.1:%d", echoPort))
	defer echoClose()
	waitForListener(t, fmt.Sprintf("127.0.0.1:%d", echoPort), 5*time.Second)

	logDir := t.TempDir()

	peerLog := filepath.Join(logDir, "peer_stderr.log")
	peerLogF, _ := os.Create(peerLog)
	args := []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code, "--allow-tunnel")...)
	peerCmd := exec.Command(qvoleBin, args...)
	peerCmd.Stderr = peerLogF
	peerCmd.Env = os.Environ()
	if err := peerCmd.Start(); err != nil {
		t.Fatalf("start peer: %v", err)
	}
	defer peerCmd.Process.Kill()
	peerLogF.Close()
	time.Sleep(2 * time.Second)

	initLog := filepath.Join(logDir, "init_stderr.log")
	initLogF, _ := os.Create(initLog)
	args = []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code,
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", fwdPort, echoPort))...)
	initCmd := exec.Command(qvoleBin, args...)
	initCmd.Stderr = initLogF
	initCmd.Env = os.Environ()
	if err := initCmd.Start(); err != nil {
		t.Fatalf("start init: %v", err)
	}
	defer initCmd.Process.Kill()
	initLogF.Close()

	fwdAddr := fmt.Sprintf("127.0.0.1:%d", fwdPort)
	waitForListener(t, fwdAddr, 15*time.Second)

	conn, err := net.Dial("tcp", fwdAddr)
	if err != nil {
		initData, _ := os.ReadFile(initLog)
		peerData, _ := os.ReadFile(peerLog)
		t.Fatalf("%v\nINIT: %s\nPEER: %s", err, string(initData), string(peerData))
	}
	defer conn.Close()

	payload := []byte("hello-local-forward")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 4096)
	readDeadline := time.Now().Add(10 * time.Second)
	for {
		conn.SetReadDeadline(time.Now().Add(testReadPoll))
		n, readErr := conn.Read(buf)
		if n > 0 {
			if bytes.Contains(buf[:n], payload) {
				return
			}
		}
		if readErr != nil {
			if isTimeout(readErr) {
				if time.Now().After(readDeadline) {
					t.Fatalf("read: timeout (got partial %q)", string(buf[:n]))
				}
				continue
			}
			t.Fatalf("read: %v (got %q)", readErr, string(buf[:n]))
		}
		if time.Now().After(readDeadline) {
			t.Fatalf("read: timeout (got partial %q)", string(buf[:n]))
		}
	}
}

func TestTunnelAllowReject(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	fwdPort := 19031
	code := "9930-allow-tunnel-reject"

	peerLog := filepath.Join(t.TempDir(), "peer_stderr.log")
	peerLogF, _ := os.Create(peerLog)

	args := []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code)...)
	peerCmd := exec.Command(qvoleBin, args...)
	peerCmd.Stdin = strings.NewReader("")
	peerCmd.Stderr = peerLogF
	if err := peerCmd.Start(); err != nil {
		t.Fatalf("start peer: %v", err)
	}
	defer peerCmd.Process.Kill()
	peerLogF.Close()
	time.Sleep(2 * time.Second)

	args = []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code,
		"-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:9999", fwdPort))...)
	initCmd := exec.Command(qvoleBin, args...)
	initCmd.Stdin = strings.NewReader("")
	initCmd.Stderr = &logWriter{t: t, prefix: "[init-stderr] "}
	if err := initCmd.Start(); err != nil {
		t.Fatalf("start init: %v", err)
	}
	defer initCmd.Process.Kill()
	time.Sleep(4 * time.Second)

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", fwdPort), 3*time.Second)
	if err != nil {
		return
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(2 * time.Second))
	conn.Write([]byte("test"))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	if err != nil {
		return
	}

	t.Fatal("tunnel allowed data flow without --allow-tunnel on recipient")
}

func TestTunnelMixedLR(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	lPort := 19034
	rPort := 19035
	code := "9933-mixed-lr-test"

	args := []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code, "--allow-tunnel")...)
	peerCmd := exec.Command(qvoleBin, args...)
	peerCmd.Stdin = strings.NewReader("")
	peerCmd.Stderr = &logWriter{t: t, prefix: "[peer-stderr] "}
	if err := peerCmd.Start(); err != nil {
		t.Fatalf("start peer: %v", err)
	}
	defer peerCmd.Process.Kill()
	time.Sleep(2 * time.Second)

	args = []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code,
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:9999", lPort),
		"-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:9999", rPort))...)
	initCmd := exec.Command(qvoleBin, args...)
	initCmd.Stdin = strings.NewReader("")
	initCmd.Stderr = &logWriter{t: t, prefix: "[init-stderr] "}
	if err := initCmd.Start(); err != nil {
		t.Fatalf("start init: %v", err)
	}
	defer initCmd.Process.Kill()
	time.Sleep(4 * time.Second)

	checkPort := func(port int, label string) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
		if err != nil {
			t.Errorf("%s port %d not listening: %v", label, port, err)
			return
		}
		conn.Close()
	}

	checkPort(lPort, "-L")
	checkPort(rPort, "-R")
}

func TestTunnelTargetDown(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	fwdPort := 19037
	code := "9936-target-down-test"

	peerLog := filepath.Join(t.TempDir(), "peer_stderr.log")
	peerLogF, _ := os.Create(peerLog)
	args := []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code, "--allow-tunnel")...)
	peerCmd := exec.Command(qvoleBin, args...)
	peerCmd.Stdin = strings.NewReader("")
	peerCmd.Stderr = peerLogF
	if err := peerCmd.Start(); err != nil {
		t.Fatalf("start peer: %v", err)
	}
	defer peerCmd.Process.Kill()
	peerLogF.Close()
	time.Sleep(2 * time.Second)

	initLog := filepath.Join(t.TempDir(), "init_stderr.log")
	initLogF, _ := os.Create(initLog)
	args = []string{"tunnel"}
	args = append(args, clientModeArgs(relayAddr, "--code", code,
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:19999", fwdPort))...)
	initCmd := exec.Command(qvoleBin, args...)
	initCmd.Stdin = strings.NewReader("")
	initCmd.Stderr = initLogF
	if err := initCmd.Start(); err != nil {
		t.Fatalf("start init: %v", err)
	}
	defer initCmd.Process.Kill()
	initLogF.Close()

	fwdAddr := fmt.Sprintf("127.0.0.1:%d", fwdPort)
	waitForListener(t, fwdAddr, 15*time.Second)

	conn, err := net.Dial("tcp", fwdAddr)
	if err != nil {
		initData, _ := os.ReadFile(initLog)
		peerData, _ := os.ReadFile(peerLog)
		t.Fatalf("%v\nINIT: %s\nPEER: %s", err, string(initData), string(peerData))
	}

	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write([]byte("test"))
	buf := make([]byte, 1024)
	_, readErr := conn.Read(buf)
	conn.Close()

	if readErr == nil {
		t.Error("expected error when tunnel target is down, got successful read")
	}
}
