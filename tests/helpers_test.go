package tests

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testTimeout  = 25 * time.Second
	testReadPoll = 3 * time.Second
	testBufSize  = 1500
)

var nextTestPort atomic.Int64

func init() {
	nextTestPort.Store(19000)
}

func testRelay(t *testing.T) (relayAddr string, cleanup func()) {
	t.Helper()
	port := int(nextTestPort.Add(1))
	return fmt.Sprintf("127.0.0.1:%d", port), startRelay(t, port)
}

func startRelay(t *testing.T, port int) func() {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	logFile := filepath.Join(t.TempDir(), "relay.log")
	f, err := os.Create(logFile)
	if err != nil {
		t.Fatalf("create relay log: %v", err)
	}
	f.Close()

	args := []string{"relay", "--listen", addr}
	cmd := exec.Command(qvoleBin, args...)
	cmd.Stdout = &logWriter{t: t, prefix: "[relay] "}
	cmd.Stderr = &logWriter{t: t, prefix: "[relay] "}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start relay: %v", err)
	}

	if !waitForRelay(addr, 8*time.Second) {
		cmd.Process.Kill()
		data, _ := os.ReadFile(logFile)
		t.Fatalf("relay did not start within 8s\n%s", string(data))
	}

	return func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}
}

func clientModeArgs(addr string, extra ...string) []string {
	args := []string{"--relay", addr}
	args = append(args, extra...)
	return args
}

func pipeArgs(addr string, extra ...string) []string {
	args := []string{"pipe", "--relay", addr}
	if len(extra) > 0 {
		args = append(args, "--code", extra[0])
		args = append(args, extra[1:]...)
	}
	return args
}

type logWriter struct {
	t      *testing.T
	prefix string
}

func (w *logWriter) Write(p []byte) (int, error) {
	lines := strings.TrimRight(string(p), "\n")
	for _, line := range strings.Split(lines, "\n") {
		w.t.Logf("%s%s", w.prefix, line)
	}
	return len(p), nil
}

func waitForRelay(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return false
	}
	for time.Now().Before(deadline) {
		conn, err := net.DialUDP("udp", nil, udpAddr)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		msg := []byte("REG waitfortest\n")
		conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write(msg); err != nil {
			conn.Close()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		buf := make([]byte, testBufSize)
		n, err := conn.Read(buf)
		conn.Close()
		if err == nil && n > 0 && strings.HasPrefix(string(buf[:n]), "REGD") {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func isTimeout(err error) bool {
	if ne, ok := err.(net.Error); ok {
		return ne.Timeout()
	}
	return os.IsTimeout(err)
}

func readUntilContains(f *os.File, needle []byte, timeout time.Duration) (bool, string) {
	buf := make([]byte, 65536)
	var acc bytes.Buffer
	deadline := time.Now().Add(timeout)
	for {
		f.SetReadDeadline(time.Now().Add(testReadPoll))
		n, err := f.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			if bytes.Contains(acc.Bytes(), needle) {
				return true, acc.String()
			}
		}
		if err != nil {
			if isTimeout(err) {
				if time.Now().After(deadline) {
					return false, acc.String()
				}
				continue
			}
			return false, acc.String()
		}
		if time.Now().After(deadline) {
			return false, acc.String()
		}
	}
}

func captureCode(t *testing.T, logPath string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	prefix := []byte("QVOLE_CODE=")
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		idx := bytes.Index(data, prefix)
		if idx >= 0 {
			rest := data[idx+len(prefix):]
			end := bytes.IndexByte(rest, '\n')
			if end < 0 {
				end = len(rest)
			}
			code := strings.TrimSpace(string(rest[:end]))
			if code != "" {
				return code
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

func startEchoServer(t *testing.T, addr string) (net.Listener, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("echo listen %s: %v", addr, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln, func() { ln.Close() }
}

// waitForListener polls addr until a TCP connection is accepted or timeout expires.
func waitForListener(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("port %s not listening after %v", addr, timeout)
}

func mustPipe(t testing.TB) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	return r, w
}

func blockingStdin() (*os.File, func()) {
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	return r, func() { w.Close() }
}

func waitForExit(cmd *exec.Cmd, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
