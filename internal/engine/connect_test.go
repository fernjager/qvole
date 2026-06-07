package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/fernjager/qvole/spake2"
)

var testFingerprint = []byte{
	0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
	0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
	0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
	0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
}

func TestDetectOutboundAddr_SpecificIP(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	addr := detectOutboundAddr(conn)

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("expected 127.0.0.1, got %s", host)
	}
	if port == "" {
		t.Fatal("empty port")
	}
}

func TestDetectOutboundAddr_UnspecifiedFallback(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	addr := detectOutboundAddr(conn)

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	// Should have resolved to a real local IP (not 0.0.0.0)
	if host == "0.0.0.0" || host == "::" {
		t.Fatalf("expected resolved IP, got %s", host)
	}
	if port == "" {
		t.Fatal("empty port")
	}
}

func TestDetectOutboundAddr_IPv6(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Skipf("IPv6 not available: %v", err)
	}
	defer conn.Close()

	addr := detectOutboundAddr(conn)

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if port == "" {
		t.Fatal("empty port")
	}
}

func TestConnectPeer_UnresolvableRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := ConnectPeer(ctx, "nonexistent.invalid:9999", "9901-test-code-connect")
	if err == nil {
		t.Fatal("expected error for unresolvable relay")
	}
}

func TestRegisterAndExchange_SPAKE2PayloadTooShort(t *testing.T) {
	relayConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	defer relayConn.Close()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer udpConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Start registerAndExchange in background; it will send REG and SPAKE2
	resultCh := make(chan error, 1)
	go func() {
		_, _, _, err := registerAndExchange(ctx, udpConn, "testroom1", "test-code-for-spake2", []byte("fingerprint"), PeerConfig{})
		resultCh <- err
	}()

	// Read relay messages from the peer
	readBuf := make([]byte, 1500)
	for i := 0; i < 20; i++ {
		relayConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, src, err := relayConn.ReadFromUDP(readBuf)
		if err != nil {
			continue
		}
		line := string(readBuf[:n])

		// Respond to REG with REGD
		if len(line) > 4 && line[:4] == "REG " {
			relayConn.WriteTo([]byte(fmt.Sprintf("REGD testroom1 %s\n", src.String())), src)
		}

		// Send back a too-short SPAKE2 payload (only 30 bytes)
		if len(line) > 4 && line[:4] == "MSG " {
			shortPayload := hex.EncodeToString(make([]byte, 30))
			relayConn.WriteTo([]byte("MSGD spake2 "+shortPayload+"\n"), src)
		}
	}

	select {
	case <-resultCh:
		// Should either timeout or return error; both are acceptable
	case <-time.After(5 * time.Second):
		cancel()
	}
}

func TestRegisterAndExchange_ConfirmBeforeSPAKE2(t *testing.T) {
	relayConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	defer relayConn.Close()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer udpConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		_, _, _, err := registerAndExchange(ctx, udpConn, "testroom2", "test-code-for-spake2", []byte("fingerprint"), PeerConfig{})
		resultCh <- err
	}()

	// Send a confirm message before any SPAKE2 exchange; should not crash
	readBuf := make([]byte, 1500)
	for i := 0; i < 20; i++ {
		relayConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, src, err := relayConn.ReadFromUDP(readBuf)
		if err != nil {
			continue
		}
		line := string(readBuf[:n])

		if len(line) > 4 && line[:4] == "REG " {
			relayConn.WriteTo([]byte(fmt.Sprintf("REGD testroom2 %s\n", src.String())), src)
		}

		// Immediately send a confirm payload before sending SPAKE2
		if len(line) > 4 && line[:4] == "MSG " {
			fakeConfirm := hex.EncodeToString(make([]byte, ConfirmPayloadSize))
			relayConn.WriteTo([]byte("MSGD confirm "+fakeConfirm+"\n"), src)
		}
	}

	select {
	case <-resultCh:
		// Should timeout or error; the confirm-before-spake2 should be ignored
	case <-time.After(5 * time.Second):
		cancel()
	}
}

func TestRegisterAndExchange_ConfirmPayloadTooShort(t *testing.T) {
	relayConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	defer relayConn.Close()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer udpConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		_, _, _, err := registerAndExchange(ctx, udpConn, "testroom3", "test-code-for-spake2", []byte("fingerprint"), PeerConfig{})
		resultCh <- err
	}()

	peerState, _ := spake2.NewState("test-code-for-spake2")
	peerPointM := peerState.BlindedBytesM()
	peerPointN := peerState.BlindedBytesN()
	peerFingerprint := make([]byte, 32)

	spake2Payload := make([]byte, 0, spake2PayloadLen)
	spake2Payload = append(spake2Payload, peerPointM...)
	spake2Payload = append(spake2Payload, peerPointN...)
	spake2Payload = append(spake2Payload, peerFingerprint...)
	hexSpake2 := hex.EncodeToString(spake2Payload)

	readBuf := make([]byte, 1500)
	receivedSpake2 := false
	for i := 0; i < 30; i++ {
		relayConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, src, err := relayConn.ReadFromUDP(readBuf)
		if err != nil {
			continue
		}
		line := string(readBuf[:n])

		if len(line) > 4 && line[:4] == "REG " {
			relayConn.WriteTo([]byte(fmt.Sprintf("REGD testroom3 %s\n", src.String())), src)
		}

		if !receivedSpake2 && len(line) > 4 && line[:4] == "MSG " {
			// Send back the valid peer SPAKE2
			relayConn.WriteTo([]byte("MSGD spake2 "+hexSpake2+"\n"), src)
			receivedSpake2 = true
			// Now send a too-short confirm (64 bytes < ConfirmPayloadSize 160)
			shortConfirm := hex.EncodeToString(make([]byte, 64))
			relayConn.WriteTo([]byte("MSGD confirm "+shortConfirm+"\n"), src)
		}
	}

	select {
	case <-resultCh:
		// Should timeout; confirm too short should be silently dropped
	case <-time.After(5 * time.Second):
		cancel()
	}
}

func TestRegisterAndExchange_ContextCancellation(t *testing.T) {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer udpConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, _, _, err = registerAndExchange(ctx, udpConn, "testroom4", "test-code-for-spake2", []byte("fp"), PeerConfig{})
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRegisterAndExchange_MetadataPaddingWithEmbeddedNulls(t *testing.T) {
	// Test that metadata encryption/decryption roundtrips correctly even with null padding
	key := make([]byte, 32)
	addrBytes := []byte("10.0.0.1:8080")
	aad := []byte("test-aad")

	// Pad to MaxMetadataSize like registerAndExchange does
	if len(addrBytes) < MaxMetadataSize {
		padded := make([]byte, MaxMetadataSize)
		copy(padded, addrBytes)
		addrBytes = padded
	}

	enc, err := spake2.EncryptMetadata(key, aad, addrBytes)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	dec, err := spake2.DecryptMetadata(key, aad, enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	// Simulate the TrimRight that registerAndExchange does
	result := string(dec)
	result = result[:len(result)-len("\x00")]
	// Actually need to trim all trailing nulls
	for len(result) > 0 && result[len(result)-1] == 0 {
		result = result[:len(result)-1]
	}

	if result != "10.0.0.1:8080" {
		t.Fatalf("expected '10.0.0.1:8080', got %q", result)
	}
}

func TestConfirmPayloadSize(t *testing.T) {
	if ConfirmPayloadSize != 16+32+12+MaxMetadataSize+16+32 {
		t.Fatalf("ConfirmPayloadSize = %d, want %d", ConfirmPayloadSize, 16+32+12+MaxMetadataSize+16+32)
	}
	if ConfirmPayloadSize != 160 {
		t.Fatalf("ConfirmPayloadSize = %d, want 160", ConfirmPayloadSize)
	}
}

func TestDetectOutboundAddr_IPv6Unspecified(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv6zero, Port: 0})
	if err != nil {
		t.Skipf("IPv6 not available: %v", err)
	}
	defer conn.Close()

	addr := detectOutboundAddr(conn)

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	// Should have resolved to a real local IP (not ::)
	if host == "::" {
		t.Fatal("expected resolved IP, got ::")
	}
	if port == "" {
		t.Fatal("empty port")
	}
}

func TestRegisterAndExchange_ExchangeTimeout(t *testing.T) {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer udpConn.Close()

	// Use a relay that doesn't respond
	relayConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	relayConn.Close() // Close immediately so nothing responds

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, _, err = registerAndExchange(ctx, udpConn, "timeout-test", fmt.Sprintf("timeout-test-code-%d", time.Now().UnixNano()), []byte("fp"), PeerConfig{})
	if err == nil {
		t.Fatal("expected error for unresponsive relay")
	}
}

func TestRegisterAndExchange_NonTimeoutNetError(t *testing.T) {
	relayConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	defer relayConn.Close()

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Don't defer close; we'll close it mid-operation

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		_, _, _, err := registerAndExchange(ctx, udpConn, "testroom-neterr", "test-code-neterr-test", []byte("fingerprint"), PeerConfig{})
		resultCh <- err
	}()

	// Relay: respond to REG with REGD, then wait for MSGD, then close their UDP conn
	readBuf := make([]byte, 1500)
	regResponded := false
	for i := 0; i < 50; i++ {
		relayConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, src, err := relayConn.ReadFromUDP(readBuf)
		if err != nil {
			continue
		}
		line := string(readBuf[:n])

		if !regResponded && len(line) > 4 && line[:4] == "REG " {
			relayConn.WriteTo([]byte(fmt.Sprintf("REGD testroom-neterr %s\n", src.String())), src)
			regResponded = true
		}

		if regResponded && len(line) > 4 && line[:4] == "MSG " {
			// SPAKE2 sent; close peer's UDP conn to trigger non-timeout read error
			udpConn.Close()
			break
		}
	}

	select {
	case err := <-resultCh:
		if err == nil {
			t.Fatal("expected non-nil error after closing UDP conn")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("registerAndExchange did not return after UDP conn closed")
	}
}

// ---- helper ----

func setupSPAKE2Session(t *testing.T, password string) (confirmKey, encKey, myPoint, peerPoint []byte) {
	t.Helper()
	myState, err := spake2.NewState(password)
	if err != nil {
		t.Fatalf("my state: %v", err)
	}
	peerState, err := spake2.NewState(password)
	if err != nil {
		t.Fatalf("peer state: %v", err)
	}

	myM := myState.BlindedBytesM()
	peerM := peerState.BlindedBytesM()
	myN := myState.BlindedBytesN()
	peerN := peerState.BlindedBytesN()

	myIsServer := string(myM) > string(peerM)
	if myIsServer {
		myPoint = myN
		peerPoint = peerM
	} else {
		myPoint = myM
		peerPoint = peerN
	}

	peerUsedM := myIsServer
	shared, err := myState.ComputeShared(peerPoint, peerUsedM)
	if err != nil {
		t.Fatalf("compute shared: %v", err)
	}
	myState.Destroy()

	ck, ek, err := spake2.DeriveSessionKey(shared, myPoint, peerPoint)
	spake2.ZeroBytes(shared)
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}
	return ck, ek, myPoint, peerPoint
}

// ---- processSpakeMsg tests ----

func TestProcessSpakeMsg_TooShort(t *testing.T) {
	state, err := spake2.NewState("test-too-short")
	if err != nil {
		t.Fatalf("new state: %v", err)
	}
	myPointM := state.BlindedBytesM()
	myPointN := state.BlindedBytesN()

	shortHex := hex.EncodeToString(make([]byte, 30))
	_, _, _, _, _, _, err = processSpakeMsg(shortHex, state, myPointM, myPointN)
	if err == nil {
		t.Fatal("expected error for too-short payload")
	}
}

func TestProcessSpakeMsg_InvalidHex(t *testing.T) {
	state, err := spake2.NewState("test-invalid-hex")
	if err != nil {
		t.Fatalf("new state: %v", err)
	}
	myPointM := state.BlindedBytesM()
	myPointN := state.BlindedBytesN()

	_, _, _, _, _, _, err = processSpakeMsg("not-a-valid-hex-string!", state, myPointM, myPointN)
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestProcessSpakeMsg_Success(t *testing.T) {
	password := "test-process-spake-success"
	myState, err := spake2.NewState(password)
	if err != nil {
		t.Fatalf("my state: %v", err)
	}
	peerState, err := spake2.NewState(password)
	if err != nil {
		t.Fatalf("peer state: %v", err)
	}
	myPointM := myState.BlindedBytesM()
	myPointN := myState.BlindedBytesN()
	peerPointM := peerState.BlindedBytesM()
	peerPointN := peerState.BlindedBytesN()
	peerFingerprint := make([]byte, 32)
	if _, err := rand.Read(peerFingerprint); err != nil {
		t.Fatalf("rand: %v", err)
	}

	payload := make([]byte, 0, spake2PayloadLen)
	payload = append(payload, peerPointM...)
	payload = append(payload, peerPointN...)
	payload = append(payload, peerFingerprint...)
	hexBody := hex.EncodeToString(payload)

	effMyPoint, effPeerPoint, fp, isServer, ck, ek, err := processSpakeMsg(hexBody, myState, myPointM, myPointN)
	if err != nil {
		t.Fatalf("processSpakeMsg: %v", err)
	}
	if len(effMyPoint) != 65 {
		t.Fatalf("effective my point len: %d, want 65", len(effMyPoint))
	}
	if len(effPeerPoint) != 65 {
		t.Fatalf("effective peer point len: %d, want 65", len(effPeerPoint))
	}
	if len(fp) != 32 {
		t.Fatalf("fingerprint len: %d, want 32", len(fp))
	}
	if len(ck) != 32 {
		t.Fatalf("confirm key len: %d, want 32", len(ck))
	}
	if len(ek) != 32 {
		t.Fatalf("enc key len: %d, want 32", len(ek))
	}
	if !bytes.Equal(fp, peerFingerprint) {
		t.Fatal("fingerprint mismatch")
	}
	_ = isServer
}

// ---- buildConfirmPayload tests ----

func TestBuildConfirmPayload_Size(t *testing.T) {
	ck, ek, myPoint, peerPoint := setupSPAKE2Session(t, "test-confirm-size")
	myAddr := []byte("10.0.0.1:8080")

	payload, err := buildConfirmPayload(ck, ek, myPoint, peerPoint, testFingerprint, myAddr)
	if err != nil {
		t.Fatalf("buildConfirmPayload: %v", err)
	}
	if len(payload) != ConfirmPayloadSize {
		t.Fatalf("expected %d bytes, got %d", ConfirmPayloadSize, len(payload))
	}
}

func TestBuildConfirmPayload_NonceRandom(t *testing.T) {
	ck, ek, myPoint, peerPoint := setupSPAKE2Session(t, "test-nonce-random")
	myAddr := []byte("10.0.0.1:8080")

	payload1, err := buildConfirmPayload(ck, ek, myPoint, peerPoint, testFingerprint, myAddr)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	payload2, err := buildConfirmPayload(ck, ek, myPoint, peerPoint, testFingerprint, myAddr)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	nonce1 := payload1[:16]
	nonce2 := payload2[:16]
	if string(nonce1) == string(nonce2) {
		t.Fatal("nonce should be random but was identical across two calls")
	}
}

// ---- processConfirmMsg tests ----

func TestProcessConfirmMsg_TooShort(t *testing.T) {
	shortHex := hex.EncodeToString(make([]byte, 64))
	_, err := processConfirmMsg(shortHex, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for too-short confirm payload")
	}
}

func TestProcessConfirmMsg_InvalidHex(t *testing.T) {
	_, err := processConfirmMsg("not-hex!!!", nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestProcessConfirmMsg_WrongKey(t *testing.T) {
	ck, ek, myPoint, peerPoint := setupSPAKE2Session(t, "test-wrong-confirm")
	myAddr := []byte("10.0.0.1:8080")

	payload, err := buildConfirmPayload(ck, ek, myPoint, peerPoint, testFingerprint, myAddr)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	hexPayload := hex.EncodeToString(payload)

	wrongKey := make([]byte, 32)
	if _, err := rand.Read(wrongKey); err != nil {
		t.Fatalf("rand: %v", err)
	}

	_, err = processConfirmMsg(hexPayload, wrongKey, ek, myPoint, peerPoint, testFingerprint)
	if err == nil {
		t.Fatal("expected error with wrong confirm key")
	}
	if !strings.Contains(err.Error(), "confirmation mismatch") {
		t.Fatalf("expected 'confirmation mismatch', got: %v", err)
	}
}

func TestBuildConfirmPayload_AddressTooLong(t *testing.T) {
	ck, ek, myPoint, peerPoint := setupSPAKE2Session(t, "test-addr-too-long")
	myAddr := []byte(strings.Repeat("x", MaxMetadataSize+1))

	_, err := buildConfirmPayload(ck, ek, myPoint, peerPoint, testFingerprint, myAddr)
	if err == nil {
		t.Fatal("expected error for address exceeding MaxMetadataSize")
	}
	if !strings.Contains(err.Error(), "address too long") {
		t.Fatalf("expected 'address too long', got: %v", err)
	}
}

func TestProcessConfirmMsg_RoundTrip(t *testing.T) {
	ck, ek, myPoint, peerPoint := setupSPAKE2Session(t, "test-confirm-roundtrip")
	myAddr := []byte("10.0.0.1:8080")

	payload, err := buildConfirmPayload(ck, ek, myPoint, peerPoint, testFingerprint, myAddr)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	hexPayload := hex.EncodeToString(payload)

	peerAddr, err := processConfirmMsg(hexPayload, ck, ek, myPoint, peerPoint, testFingerprint)
	if err != nil {
		t.Fatalf("processConfirmMsg: %v", err)
	}
	if peerAddr != "10.0.0.1:8080" {
		t.Fatalf("expected '10.0.0.1:8080', got %q", peerAddr)
	}
}
