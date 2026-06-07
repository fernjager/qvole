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

	// Both clients register in the same room
	room := "99401-test-exchange"
	msgA := fmt.Sprintf("REG %s\n", room)
	_, err = clientA.WriteTo([]byte(msgA), relayUDPAddr)
	if err != nil {
		t.Fatalf("client A REG: %v", err)
	}

	buf := make([]byte, testBufSize)
	clientA.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := clientA.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client A read REGD: %v", err)
	}
	respA := string(buf[:n])
	if !strings.HasPrefix(respA, fmt.Sprintf("REGD %s", room)) {
		t.Fatalf("client A unexpected response: %s", respA)
	}
	t.Logf("client A got: %s", strings.TrimSpace(respA))

	// Client B registers in same room
	msgB := fmt.Sprintf("REG %s\n", room)
	_, err = clientB.WriteTo([]byte(msgB), relayUDPAddr)
	if err != nil {
		t.Fatalf("client B REG: %v", err)
	}

	clientB.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = clientB.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client B read REGD: %v", err)
	}
	respB := string(buf[:n])
	if !strings.HasPrefix(respB, fmt.Sprintf("REGD %s", room)) {
		t.Fatalf("client B unexpected response: %s", respB)
	}
	t.Logf("client B got: %s", strings.TrimSpace(respB))

	// Client A sends a MSG; client B should receive MSGD
	testPayload := hex.EncodeToString([]byte("hello-relay-test-payload-for-exchange"))
	msgSend := fmt.Sprintf("MSG %s spake2 %s\n", room, testPayload)
	_, err = clientA.WriteTo([]byte(msgSend), relayUDPAddr)
	if err != nil {
		t.Fatalf("client A MSG: %v", err)
	}

	clientB.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = clientB.ReadFrom(buf)
	if err != nil {
		t.Fatalf("client B read MSGD: %v", err)
	}
	respForward := string(buf[:n])
	expectedPrefix := fmt.Sprintf("MSGD spake2 %s", testPayload)
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
