package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fernjager/qvole/internal/engine"
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
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, 1, TunnelAllow{}, &sync.Map{}, 0)
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
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, 1, TunnelAllow{}, &sync.Map{}, 0)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleTunnelStream did not return for wrong direction")
	}
}

func TestHandleTunnelStream_AllowlistRejection(t *testing.T) {
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

	// Peer -L spec (myReqCount=0) targeting 8.8.8.8:53 with an empty
	// --allow-forward allowlist → should reject without dialing.
	done := make(chan struct{})
	go func() {
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, 0, TunnelAllow{}, &sync.Map{}, 0)
		close(done)
	}()
	waitDone(t, done, "rejected tunnel")

	// With the target allowlisted, the stream is dialed (to an unroutable
	// address, so HandleTunnelStream returns via dial failure, not rejection).
	reqs[0].TargetAddr = "192.0.2.1:9999"
	stream2, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	var header2 [2]byte
	binary.BigEndian.PutUint16(header2[:], 0)
	stream2.Write(header2[:])
	stream2.Close()

	acceptedStream2, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	done2 := make(chan struct{})
	go func() {
		HandleTunnelStream(context.Background(), acceptedStream2, reqs, 0, 0, TunnelAllow{Forward: []string{"192.0.2.1:9999"}}, &sync.Map{}, 0)
		close(done2)
	}()
	waitDone(t, done2, "allowlisted tunnel")
}

func TestAnnounceTunnelOnce(t *testing.T) {
	// Each session has its own map; the test builds its own so it is hermetic
	// and independent of any other session state.
	announced := &sync.Map{}

	const idx = 7
	const n = 32
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = announceTunnelOnce(announced, idx, "localhost:8080")
		}(i)
	}
	wg.Wait()

	firsts := 0
	for _, r := range results {
		if r {
			firsts++
		}
	}
	if firsts != 1 {
		t.Fatalf("exactly one goroutine should announce, got %d", firsts)
	}

	// A different spec announces independently; repeats stay silent.
	if !announceTunnelOnce(announced, idx+1, "localhost:80") {
		t.Fatal("new spec should announce on first connection")
	}
	if announceTunnelOnce(announced, idx+1, "localhost:80") {
		t.Fatal("spec should not announce again")
	}

	// A separate session map announces independently of the first session,
	// including for the same spec index (per-session state, not global).
	other := &sync.Map{}
	if !announceTunnelOnce(other, idx, "localhost:9090") {
		t.Fatal("a fresh session should announce again for the same spec index")
	}
	if announceTunnelOnce(other, idx, "localhost:9090") {
		t.Fatal("spec should not announce twice within one session")
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
	go HandleTunnelTCP(ctx, serverConn, remote, 42, 0)

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
	HandleTunnelTCP(ctx, serverConn, remote, 0, 0)
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
	HandleTunnelTCP(ctx, serverConn, remote, 0, 0)
	// Clean up the client conn
	clientConn.CloseWithError(0, "test")
}

func TestHandleTunnelStream_DialTimeout(t *testing.T) {
	// Bound the dial so the test is fast; the target is unroutable.
	t.Setenv("QVOLE_DIAL_TIMEOUT_MS", "250")

	clientConn, serverConn := setupQUICPair(t)

	// Use TEST-NET-1 (192.0.2.0/24) which is unroutable; dial should timeout/fail.
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
		// myReqStart=0, myReqCount=1 => this is my -R spec, so the handler
		// dials TargetAddr (previously myReqCount=0 made it return before the
		// allowlist check and the dial, asserting nothing).
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, 1, TunnelAllow{}, &sync.Map{}, 0)
		close(done)
	}()

	waitDone(t, done, "dial timeout")
}

// TestHandleTunnelStream_DialsTarget deterministically proves the handler
// reaches the dial path: it dials a real local listener. The dial-timeout
// itself is exercised by TestHandleTunnelStream_AllowlistRejection.
func TestHandleTunnelStream_DialsTarget(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	reqs := []TunnelRequest{
		{Type: "R", ListenAddr: "127.0.0.1:8080", TargetAddr: ln.Addr().String()},
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
		// my -R spec: the handler dials TargetAddr (no allowlist applies).
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, 1, TunnelAllow{}, &sync.Map{}, 0)
		close(done)
	}()

	if l, ok := ln.(*net.TCPListener); ok {
		l.SetDeadline(time.Now().Add(3 * time.Second))
	}
	tcpConn, err := ln.Accept()
	if err != nil {
		t.Fatalf("handler did not dial the target: %v", err)
	}
	tcpConn.Close()
	acceptedStream.Close()
	waitDone(t, done, "dialing tunnel")
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
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, 1, TunnelAllow{}, &sync.Map{}, 0)
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
		HandleTunnelStream(context.Background(), acceptedStream, reqs, 0, 0, TunnelAllow{}, &sync.Map{}, 0)
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

	// Close the connection with a peer application error: this is a remote
	// disconnect, not a fatal error, so RunStreamAcceptor should end cleanly.
	clientConn.CloseWithError(1, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := RunStreamAcceptor(ctx, serverConn, reqs, 0, 1, TunnelAllow{}, 200, &sync.Map{}, 0)
	if err != nil {
		t.Fatalf("expected nil error on peer disconnect, got %v", err)
	}
}

func TestRunStreamAcceptor_GuardFullContextCancel(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)
	defer clientConn.CloseWithError(0, "test")

	reqs := []TunnelRequest{
		{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
	}

	// Fill the single guard slot with a stream whose handler blocks waiting
	// for a header, then open a second stream so the acceptor parks on the
	// full guard.
	s1, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream 1: %v", err)
	}
	defer s1.Close()
	s2, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream 2: %v", err)
	}
	defer s2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunStreamAcceptor(ctx, serverConn, reqs, 0, 1, TunnelAllow{}, 1, &sync.Map{}, 0)
	}()

	// Let the acceptor accept s1 (occupying the guard) and park on s2.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunStreamAcceptor parked on full guard after context cancel")
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

	err = RunTCPListeners(ctx, serverConn, reqs, 0, 1, true, TunnelAllow{}, 0)
	if err == nil {
		t.Fatal("expected error for port already in use")
	}
}

func TestStreamIdleTimeout_Configurable(t *testing.T) {
	// Disabled: a forwarded tunnel stream is never force-closed, so long-running
	// and bulk transfers are not cut off by an unexpected disconnect.
	t.Setenv("QVOLE_STREAM_IDLE_TIMEOUT_MS", "0")
	if got := streamIdleTimeout(); got != 0 {
		t.Fatalf("stream idle timeout = %v, want 0 (disabled)", got)
	}

	// Enabled in milliseconds.
	t.Setenv("QVOLE_STREAM_IDLE_TIMEOUT_MS", "300000")
	if got := streamIdleTimeout(); got != 5*time.Minute {
		t.Fatalf("stream idle timeout = %v, want 5m", got)
	}

	// Invalid (negative) and non-numeric values fall back to disabled.
	t.Setenv("QVOLE_STREAM_IDLE_TIMEOUT_MS", "-100")
	if got := streamIdleTimeout(); got != 0 {
		t.Fatalf("negative stream idle timeout = %v, want 0", got)
	}
	t.Setenv("QVOLE_STREAM_IDLE_TIMEOUT_MS", "abc")
	if got := streamIdleTimeout(); got != 0 {
		t.Fatalf("non-numeric stream idle timeout = %v, want 0", got)
	}
}

// TestHandleTunnelTCP_ActiveTransferNotCapped verifies that with the stream idle
// timeout enabled, an actively transferring pipe is not torn down: the deadline
// is refreshed on traffic, so it survives well past the configured window.
func TestHandleTunnelTCP_ActiveTransferNotCapped(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	go HandleTunnelTCP(context.Background(), serverConn, remote, 42, time.Second)

	stream, err := clientConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	defer stream.Close()

	var header [2]byte
	if _, err := io.ReadFull(stream, header[:]); err != nil {
		t.Fatalf("read header: %v", err)
	}

	// Pump data continuously for well past the window; with a refreshed deadline
	// the pipe must survive, whereas an absolute-cap regression would tear it down.
	end := time.Now().Add(2500 * time.Millisecond)
	readBuf := make([]byte, 1)
	for time.Now().Before(end) {
		if _, err := stream.Write([]byte("x")); err != nil {
			t.Fatalf("write stream: %v", err)
		}
		local.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := local.Read(readBuf); err != nil {
			t.Fatalf("read local: %v", err)
		}
	}
}

// TestHandleTunnelTCP_SilentStreamReaped verifies that when the stream idle
// timeout is enabled, a stream that goes completely silent is reaped after the
// window (its close is observable as a read error on the peer side).
func TestHandleTunnelTCP_SilentStreamReaped(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	go HandleTunnelTCP(context.Background(), serverConn, remote, 42, 300*time.Millisecond)

	stream, err := clientConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	defer stream.Close()

	var header [2]byte
	if _, err := io.ReadFull(stream, header[:]); err != nil {
		t.Fatalf("read header: %v", err)
	}

	// No data after the header: the tunnel side must reap the silent stream
	// after the idle window, so a subsequent read returns an error.
	time.Sleep(1200 * time.Millisecond)
	stream.SetReadDeadline(time.Now().Add(2 * time.Second))
	var b [1]byte
	if _, err := stream.Read(b[:]); err == nil {
		t.Fatal("expected silent stream to be reaped (closed) after the idle window")
	}
}

func TestInitOutboundGuard_ResizesPerSession(t *testing.T) {
	// Restore the env/default-sized guard so other tests are unaffected.
	defer initOutboundGuard(engine.GetForwardMaxStreams())

	initOutboundGuard(5)
	if got := cap(getOutboundGuard()); got != 5 {
		t.Fatalf("guard capacity = %d, want 5", got)
	}

	// A later session with a different limit must re-create the guard rather
	// than keeping the first session's size.
	initOutboundGuard(9)
	if got := cap(getOutboundGuard()); got != 9 {
		t.Fatalf("guard capacity = %d, want 9", got)
	}

	// Same size keeps the existing channel (no churn for repeated sessions).
	g := getOutboundGuard()
	initOutboundGuard(9)
	if getOutboundGuard() != g {
		t.Fatal("guard channel replaced although size did not change")
	}
}
