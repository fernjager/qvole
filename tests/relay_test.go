package tests

import (
	"encoding/hex"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRelayStartup(t *testing.T) {
	addr, cleanup := testRelay(t)
	defer cleanup()

	if !waitForRelay(addr, 5*time.Second) {
		t.Fatal("relay did not respond to REG")
	}
}

func TestRelayMessageExchange(t *testing.T) {
	addr, cleanup := testRelay(t)
	defer cleanup()

	if !waitForRelay(addr, 5*time.Second) {
		t.Fatal("relay did not respond to REG")
	}

	relayUDPAddr, _ := net.ResolveUDPAddr("udp", addr)

	clientA, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("client A listen: %v", err)
	}
	defer clientA.Close()

	clientB, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("client B listen: %v", err)
	}
	defer clientB.Close()

	// Both clients register in the same room via the two-step cookie handshake.
	room := "99401-test-exchange"
	buf := make([]byte, testBufSize)

	// --- Client A: REG → REGD cookie → REG with cookie → REGD OK ---
	_, err = clientA.WriteTo([]byte("REG "+room+"\n"), relayUDPAddr)
	if err != nil {
		t.Fatalf("client A REG: %v", err)
	}
	clientA.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := clientA.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client A read REGD cookie: %v", err)
	}
	respA := strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(respA, "REGD "+room+" ") {
		t.Fatalf("client A unexpected cookie response: %s", respA)
	}
	cookieA := strings.TrimPrefix(respA, "REGD "+room+" ")
	t.Logf("client A got cookie: %s", cookieA)

	// Echo cookie to complete registration.
	_, err = clientA.WriteTo([]byte("REG "+room+" "+cookieA+"\n"), relayUDPAddr)
	if err != nil {
		t.Fatalf("client A REG+cookie: %v", err)
	}
	clientA.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = clientA.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client A read REGD OK: %v", err)
	}
	respA2 := strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(respA2, "REGD "+room+" OK ") {
		t.Fatalf("client A unexpected OK response: %s", respA2)
	}
	t.Logf("client A got: %s", respA2)

	// --- Client B: same two-step handshake ---
	_, err = clientB.WriteTo([]byte("REG "+room+"\n"), relayUDPAddr)
	if err != nil {
		t.Fatalf("client B REG: %v", err)
	}
	clientB.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = clientB.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client B read REGD cookie: %v", err)
	}
	respB := strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(respB, "REGD "+room+" ") {
		t.Fatalf("client B unexpected cookie response: %s", respB)
	}
	cookieB := strings.TrimPrefix(respB, "REGD "+room+" ")
	t.Logf("client B got cookie: %s", cookieB)

	_, err = clientB.WriteTo([]byte("REG "+room+" "+cookieB+"\n"), relayUDPAddr)
	if err != nil {
		t.Fatalf("client B REG+cookie: %v", err)
	}
	clientB.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = clientB.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client B read REGD OK: %v", err)
	}
	respB2 := strings.TrimSpace(string(buf[:n]))
	if !strings.HasPrefix(respB2, "REGD "+room+" OK ") {
		t.Fatalf("client B unexpected OK response: %s", respB2)
	}
	t.Logf("client B got: %s", respB2)

	// Client A sends a MSG; client B should receive MSGD.
	testPayload := hex.EncodeToString([]byte("hello-relay-test-payload-for-exchange"))
	_, err = clientA.WriteTo([]byte("MSG "+room+" spake2 "+testPayload+"\n"), relayUDPAddr)
	if err != nil {
		t.Fatalf("client A MSG: %v", err)
	}

	clientB.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = clientB.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client B read MSGD: %v", err)
	}
	respForward := string(buf[:n])
	expectedPrefix := "MSGD spake2 " + testPayload
	if !strings.HasPrefix(respForward, expectedPrefix) {
		t.Fatalf("client B expected MSGD with payload, got: %s", strings.TrimSpace(respForward))
	}
	t.Logf("client B got forwarded: %s", strings.TrimSpace(respForward))
}

func TestRelayGracefulShutdown(t *testing.T) {
	port := 19402
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	cmd := exec.Command(qvoleBin, "relay", "--listen", addr)
	cmd.Stderr = &logWriter{t: t, prefix: "[relay-stderr] "}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start relay: %v", err)
	}

	if !waitForRelay(addr, 5*time.Second) {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("relay did not start")
	}

	// Send SIGTERM for graceful shutdown
	if err := cmd.Process.Signal(gracefulSignal()); err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("signal: %v", err)
	}

	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Log("relay exited cleanly on SIGTERM")
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("relay did not exit within 5s after SIGTERM")
	}
}

func TestRelayInvalidListen(t *testing.T) {
	cmd := exec.Command(qvoleBin, "relay", "--listen", "invalid:999999:extra")
	cmd.Stderr = &logWriter{t: t, prefix: "[relay-err] "}
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected error for invalid listen address")
	}
	t.Logf("relay failed as expected: %v", err)
}
