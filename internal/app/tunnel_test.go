package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestHandleTunnelStream_InvalidIndex(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	reqs := []TunnelRequest{
		{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "127.0.0.1:80"},
	}

	stream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	// Write an out-of-bounds index
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(reqs)+5))
	stream.Write(header[:])
	stream.Close()

	acceptedStream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	// HandleTunnelStream should return quickly without panicking
	done := make(chan struct{})
	go func() {
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 1, true)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleTunnelStream did not return for invalid index")
	}
}

func TestHandleTunnelStream_WrongDirection(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	reqs := []TunnelRequest{
		{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
	}

	stream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	var header [2]byte
	binary.BigEndian.PutUint16(header[:], 0)
	stream.Write(header[:])
	stream.Close()

	acceptedStream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	// Initiator has type L, so shouldDial = (spec.Type == "R") = false
	done := make(chan struct{})
	go func() {
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 1, true)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleTunnelStream did not return for wrong direction")
	}
}

func TestHandleTunnelStream_AllowTunnelRejection(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	reqs := []TunnelRequest{
		{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
	}

	stream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	var header [2]byte
	binary.BigEndian.PutUint16(header[:], 0)
	stream.Write(header[:])
	stream.Close()

	acceptedStream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	// Peer spec (myReqCount=0), allowTunnel=false → should reject without dialing
	done := make(chan struct{})
	go func() {
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("HandleTunnelStream did not return for rejected tunnel")
	}
}

func TestHandleTunnelTCP_SpecIndexUint16Truncation(t *testing.T) {
	// Verify that binary.BigEndian.PutUint16 truncates large indices to uint16.
	// HandleTunnelTCP writes specIdx as uint16; values > 65535 silently truncate.
	// This test verifies the truncation behavior by checking that a value
	// that exceeds uint16 max just wraps around.
	var header [2]byte
	largeIdx := 65536
	binary.BigEndian.PutUint16(header[:], uint16(largeIdx))
	result := binary.BigEndian.Uint16(header[:])
	if result != 0 {
		t.Errorf("uint16(65536) = %d, want 0 (wraps around)", result)
	}

	// Also test specIdx exactly at uint16 max
	maxIdx := 65535
	binary.BigEndian.PutUint16(header[:], uint16(maxIdx))
	result = binary.BigEndian.Uint16(header[:])
	if result != 65535 {
		t.Errorf("uint16(65535) = %d, want 65535", result)
	}
}

func TestHandleTunnelTCP_Success(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	ctx := context.Background()

	// Start HandleTunnelTCP on server side; it will open a QUIC stream
	// pointing at the client conn and copy data to/from the TCP side.
	go HandleTunnelTCP(ctx, serverConn, remote, 42)

	// Client side: accept the stream, verify header, exchange data
	stream, err := clientConn.AcceptStream(ctx)
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	defer stream.Close()

	var header [2]byte
	if _, err = io.ReadFull(stream, header[:]); err != nil {
		t.Fatalf("read header: %v", err)
	}
	idx := binary.BigEndian.Uint16(header[:])
	if idx != 42 {
		t.Fatalf("expected spec index 42, got %d", idx)
	}

	// Write through stream → should reach local via bidirectionalCopy
	payload := []byte("hello-tunnel-tcp")
	if _, err = stream.Write(payload); err != nil {
		t.Fatalf("write stream: %v", err)
	}

	// Read from the local side (the TCP end)
	buf := make([]byte, 100)
	local.SetReadDeadline(time.Now().Add(2 * time.Second))
	var n int
	n, err = local.Read(buf)
	if err != nil {
		t.Fatalf("read local: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Fatalf("expected %q, got %q", payload, buf[:n])
	}

	// Write back through local → should reach stream via bidirectionalCopy
	response := []byte("goodbye-tunnel-tcp")
	if _, err = local.Write(response); err != nil {
		t.Fatalf("write local: %v", err)
	}

	stream.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = stream.Read(buf)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if string(buf[:n]) != string(response) {
		t.Fatalf("expected %q, got %q", response, buf[:n])
	}
}

func TestHandleTunnelTCP_OpenStreamError(t *testing.T) {
	_, serverConn := setupQUICPair(t)
	// Close the server conn so OpenStreamSync fails
	serverConn.CloseWithError(0, "test")

	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancelled ctx triggers immediate error on OpenStreamSync

	// Should not panic, just return
	HandleTunnelTCP(ctx, serverConn, remote, 0)
}

func TestHandleTunnelTCP_WriteHeaderError(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	// Close the server conn so that writing the header fails
	serverConn.CloseWithError(0, "test")

	ctx := context.Background()
	// Should not panic; OpenStreamSync will fail or write will fail
	HandleTunnelTCP(ctx, serverConn, remote, 0)
	// Clean up the client conn
	clientConn.CloseWithError(0, "test")
}

func TestHandleTunnelStream_DialTimeout(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	// Use TEST-NET-1 (192.0.2.0/24) which is unroutable; dial should timeout/fail
	reqs := []TunnelRequest{
		{Type: "R", ListenAddr: "127.0.0.1:8080", TargetAddr: "192.0.2.1:9999"},
	}

	stream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	var header [2]byte
	binary.BigEndian.PutUint16(header[:], 0)
	stream.Write(header[:])
	stream.Close()

	acceptedStream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	done := make(chan struct{})
	go func() {
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, true)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("HandleTunnelStream did not return after dial timeout")
	}
}

func TestHandleTunnelStream_ShortHeader(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	reqs := []TunnelRequest{
		{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
	}

	stream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	stream.Write([]byte{0x00})
	stream.Close()

	acceptedStream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	done := make(chan struct{})
	go func() {
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 1, true)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleTunnelStream did not return for short header")
	}
}

func TestHandleTunnelStream_PeerRType(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	reqs := []TunnelRequest{
		{Type: "R", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
	}

	stream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	var header [2]byte
	binary.BigEndian.PutUint16(header[:], 0)
	stream.Write(header[:])
	stream.Close()

	acceptedStream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}

	// myReqCount=0: this is a peer spec; req.Type == "R" && !myReq → return without dialing
	done := make(chan struct{})
	go func() {
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, true)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleTunnelStream did not return for peer R-type")
	}
}

func TestTunnelRunStreamAcceptor_ConnectionClosed(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	reqs := []TunnelRequest{
		{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
	}

	// Close the connection so AcceptStream returns an error
	clientConn.CloseWithError(1, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := RunStreamAcceptor(ctx, serverConn, reqs, 1, true)
	if err == nil {
		t.Fatal("expected error from RunStreamAcceptor with closed connection")
	}
}

func TestTunnelRunTCPListeners_BindFailure(t *testing.T) {
	// First, bind a port to create a conflict
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	usedPort := ln.Addr().(*net.TCPAddr).Port

	_, serverConn := setupQUICPair(t)
	defer serverConn.CloseWithError(0, "test")

	reqs := []TunnelRequest{
		{Type: "L", ListenAddr: fmt.Sprintf("127.0.0.1:%d", usedPort), TargetAddr: "8.8.8.8:53"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = RunTCPListeners(ctx, serverConn, reqs, 1, true, true)
	if err == nil {
		t.Fatal("expected error for port already in use")
	}
}
