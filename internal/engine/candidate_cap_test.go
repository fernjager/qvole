package engine

import (
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// randomSpakePayload returns a well-formed SPAKE2 payload (two valid P-256
// points plus a random fingerprint) that does not correspond to the code. It
// stands in for an attacker's guess without paying for PBKDF2.
func randomSpakePayload(t *testing.T) []byte {
	t.Helper()
	payload := make([]byte, 0, spake2PayloadLen)
	for i := 0; i < 2; i++ {
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			t.Fatal(err)
		}
		x, y := elliptic.P256().ScalarBaseMult(k)
		payload = append(payload, elliptic.Marshal(elliptic.P256(), x, y)...)
	}
	fp := make([]byte, fingerprintSize)
	if _, err := rand.Read(fp); err != nil {
		t.Fatal(err)
	}
	return append(payload, fp...)
}

// TestRegisterAndExchange_CandidateCapBoundsGuesses is the regression test for
// online guess amplification: each distinct point answered with a confirm is
// one offline-checkable guess at the code. A flood of points must yield at
// most maxCandidates confirms and then abort the exchange, never evict and
// keep answering.
func TestRegisterAndExchange_CandidateCapBoundsGuesses(t *testing.T) {
	relayConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	defer relayConn.Close()

	udpConn, err := net.DialUDP("udp", nil, relayConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer udpConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const room = "capt"
	resultCh := make(chan error, 1)
	go func() {
		_, _, _, _, err := registerAndExchange(ctx, udpConn, room, "candidate-cap-test-code", testFingerprint, PeerConfig{})
		resultCh <- err
	}()

	confirms := make(map[string]bool)
	flooded := false
	buf := make([]byte, readBufferSize)
	for {
		select {
		case err := <-resultCh:
			if !errors.Is(err, errTooManyCandidates) {
				t.Fatalf("expected errTooManyCandidates, got %v", err)
			}
			if len(confirms) > maxCandidates {
				t.Fatalf("answered %d distinct points; cap is %d", len(confirms), maxCandidates)
			}
			return
		default:
		}

		relayConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, src, err := relayConn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(buf[:n]))
		switch {
		case strings.HasPrefix(line, "REG "):
			relayConn.WriteToUDP([]byte("REGD "+room+" OK "+src.String()+"\n"), src)
		case strings.HasPrefix(line, "MSG "+room+" confirm "):
			confirms[strings.TrimPrefix(line, "MSG "+room+" confirm ")] = true
		case strings.HasPrefix(line, "MSG "+room+" spake2 ") && !flooded:
			flooded = true
			for i := 0; i < maxCandidates*4; i++ {
				msg := "MSGD spake2 " + hex.EncodeToString(randomSpakePayload(t)) + "\n"
				relayConn.WriteToUDP([]byte(msg), src)
			}
		}
	}
}
