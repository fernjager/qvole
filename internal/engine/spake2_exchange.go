package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/fernjager/qvole/internal/util"
	"github.com/fernjager/qvole/spake2"
)

const (
	pointLen            = 65
	spake2PayloadLen    = pointLen*2 + fingerprintSize
	fingerprintSize     = 32
	maxCandidates       = 50
	maxBufferedConfirms = 200
	readBufferSize      = 1500
	// maxInboundMSGDPerSecond bounds work an on-path/malicious relay can
	// force: each SPAKE2 point costs P-256 operations and each confirm can be
	// tried against every candidate. Legitimate room traffic is far below this
	// (the relay caps each client at 10 msg/s and a room at 20 clients).
	maxInboundMSGDPerSecond = 500
	spake2ResendInterval    = 2 * time.Second
	confirmResendInterval   = 2 * time.Second
	exchangeReadDeadline    = 1 * time.Second
)

// processSpakeMsg parses the peer's SPAKE2 payload (myPointM || myPointN || fingerprint),
// determines the protocol role by comparing M-points, and selects the effective points:
//
//	Server (larger M-point) uses N; Client uses M.
//
// Returns the effective points, fingerprint, role, and derived session keys.
func processSpakeMsg(body string, state *spake2.State, myPointM, myPointN, myFingerprint []byte) (effectiveMyPoint, effectivePeerPoint, peerFingerprint []byte, isServer bool, confirmKey, encKey []byte, err error) {
	peerPayload, err := hex.DecodeString(body)
	if err != nil {
		return nil, nil, nil, false, nil, nil, err
	}
	if len(peerPayload) < spake2PayloadLen {
		return nil, nil, nil, false, nil, nil, fmt.Errorf("spake2 payload too short")
	}
	peerPointM := peerPayload[:pointLen]
	peerPointN := peerPayload[pointLen : pointLen*2]
	peerFingerprint = peerPayload[pointLen*2 : spake2PayloadLen]
	if bytes.Equal(myFingerprint, peerFingerprint) {
		return nil, nil, nil, false, nil, nil, fmt.Errorf("spake2: peer sent reflected certificate fingerprint")
	}

	if cmp := bytes.Compare(myPointM, peerPointM); cmp == 0 {
		return nil, nil, nil, false, nil, nil, fmt.Errorf("spake2: peer sent reflected point")
	} else {
		isServer = cmp > 0
	}

	if isServer {
		effectiveMyPoint = myPointN
		effectivePeerPoint = peerPointM
	} else {
		effectiveMyPoint = myPointM
		effectivePeerPoint = peerPointN
	}

	peerUsedM := isServer
	shared, err := state.ComputeShared(effectivePeerPoint, peerUsedM)
	if err != nil {
		return nil, nil, nil, false, nil, nil, err
	}
	ck, ek, err := spake2.DeriveSessionKey(shared, effectiveMyPoint, effectivePeerPoint, myFingerprint, peerFingerprint)
	spake2.ZeroBytes(shared)
	if err != nil {
		return nil, nil, nil, false, nil, nil, fmt.Errorf("spake2 derive key: %w", err)
	}
	return effectiveMyPoint, effectivePeerPoint, peerFingerprint, isServer, ck, ek, nil
}

func buildConfirmPayload(confirmKey, encKey, myPoint, peerPoint, myFingerprint, peerFingerprint, myAddr []byte) ([]byte, error) {
	aad := spake2.SessionAAD(myPoint, peerPoint)
	confirmNonce := make([]byte, 16)
	if _, err := rand.Read(confirmNonce); err != nil {
		return nil, fmt.Errorf("rand confirm nonce: %w", err)
	}
	confirm, err := spake2.ComputeConfirm(confirmKey, myPoint, peerPoint, confirmNonce, myFingerprint, peerFingerprint)
	if err != nil {
		return nil, fmt.Errorf("compute confirm: %w", err)
	}
	addrBytes := []byte(myAddr)
	if len(addrBytes) > MaxMetadataSize {
		return nil, fmt.Errorf("address too long: %d bytes (max %d)", len(addrBytes), MaxMetadataSize)
	}
	if len(addrBytes) < MaxMetadataSize {
		padded := make([]byte, MaxMetadataSize)
		copy(padded, addrBytes)
		addrBytes = padded
	}
	encAddr, err := spake2.EncryptMetadata(encKey, aad, addrBytes)
	spake2.ZeroBytes(addrBytes)
	if err != nil {
		return nil, fmt.Errorf("encrypt addr: %w", err)
	}
	cp := make([]byte, 0, ConfirmPayloadSize)
	cp = append(cp, confirmNonce...)
	cp = append(cp, confirm...)
	cp = append(cp, encAddr...)
	if padLen := ConfirmPayloadSize - len(cp); padLen > 0 {
		pad := make([]byte, padLen)
		if _, err := rand.Read(pad); err != nil {
			return nil, fmt.Errorf("rand pad: %w", err)
		}
		cp = append(cp, pad...)
	}
	return cp, nil
}

func processConfirmMsg(hexBody string, confirmKey, encKey, myPoint, peerPoint, myFingerprint, peerFingerprint []byte) (peerAddr string, err error) {
	peerConfirmPayload, err := hex.DecodeString(hexBody)
	if err != nil {
		return "", err
	}
	if len(peerConfirmPayload) < ConfirmPayloadSize {
		return "", fmt.Errorf("confirm payload too short")
	}
	peerNonce := peerConfirmPayload[:confirmNonceSize]
	peerConfirm := peerConfirmPayload[confirmNonceSize : confirmNonceSize+confirmHMACSize]
	peerEncAddr := peerConfirmPayload[confirmNonceSize+confirmHMACSize : confirmNonceSize+confirmHMACSize+EncryptedAddrSize]

	if !spake2.VerifyConfirm(confirmKey, myPoint, peerPoint, peerNonce, myFingerprint, peerFingerprint, peerConfirm) {
		return "", fmt.Errorf("spake2 confirmation mismatch")
	}
	aad := spake2.SessionAAD(myPoint, peerPoint)
	peerAddrBytes, err := spake2.DecryptMetadata(encKey, aad, peerEncAddr)
	if err != nil {
		return "", fmt.Errorf("decrypt peer addr: %w", err)
	}
	return strings.TrimRight(string(peerAddrBytes), "\x00"), nil
}

// printableASCII reports whether s consists only of visible ASCII bytes. Used
// to reject relay-supplied address strings that would otherwise inject
// control characters into logs or be passed to the resolver.
func printableASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 33 || s[i] > 126 {
			return false
		}
	}
	return true
}

func detectOutboundAddr(udpConn *net.UDPConn) string {
	myAddr := udpConn.LocalAddr().String()
	host, port, err := net.SplitHostPort(myAddr)
	if err != nil {
		return myAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsUnspecified() {
		return myAddr
	}
	return net.JoinHostPort("localhost", port)
}

type peerCandidate struct {
	point, fingerprint, confirmKey, encKey []byte
	confirmPayload                         []byte
	isServer                               bool
	effectiveMyPoint                       []byte
}

func registerAndExchange(ctx context.Context, udpConn *net.UDPConn, room, code string, myFingerprint []byte, cfg PeerConfig) (peerAddr string, peerFingerprint []byte, isServer bool, err error) {
	state, err := spake2.NewState(code)
	if err != nil {
		return "", nil, false, fmt.Errorf("spake2 state: %w", err)
	}
	myPointM := state.BlindedBytesM()
	myPointN := state.BlindedBytesN()
	myAddr := detectOutboundAddr(udpConn)
	util.LogRelay.Printf("Local address %s", util.Bold(myAddr))

	spake2Payload := make([]byte, 0, spake2PayloadLen)
	spake2Payload = append(spake2Payload, myPointM...)
	spake2Payload = append(spake2Payload, myPointN...)
	spake2Payload = append(spake2Payload, myFingerprint...)

	candidates := make(map[string]*peerCandidate)
	confirmLastSent := make(map[string]time.Time)
	bufferedConfirms := make([]string, 0, maxBufferedConfirms)
	var allKeys [][]byte

	defer func() {
		state.Destroy()
		for _, k := range allKeys {
			spake2.ZeroBytes(k)
		}
	}()

	exchangeDeadline := cfg.exchangeDeadline()
	spake2Resend := cfg.spake2Resend()
	confirmResend := cfg.confirmResend()
	readDeadline := cfg.readDeadline()
	regInterval := cfg.regInterval()
	readBuf := make([]byte, readBufferSize)
	deadline := time.Now().Add(exchangeDeadline)
	lastSpake2Sent := time.Time{}
	lastRegSent := time.Time{}
	lastExtAddrLog := time.Time{}
	inboundWindow := time.Time{}
	inboundCount := 0
	receivedREGD := false
	pendingCookie := "" // hex cookie from relay REGD challenge

	for {
		select {
		case <-ctx.Done():
			return "", nil, false, ctx.Err()
		default:
		}
		if time.Now().After(deadline) {
			return "", nil, false, fmt.Errorf("timeout exchanging with peer")
		}

		if time.Since(lastRegSent) > regInterval {
			msg := "REG " + room + "\n"
			if pendingCookie != "" {
				msg = "REG " + room + " " + pendingCookie + "\n"
			}
			udpConn.Write([]byte(msg))
			lastRegSent = time.Now()
		}

		if receivedREGD && (lastSpake2Sent.IsZero() || time.Since(lastSpake2Sent) > spake2Resend) {
			udpConn.Write([]byte(fmt.Sprintf("MSG %s spake2 %x\n", room, spake2Payload)))
			lastSpake2Sent = time.Now()
		}

		for pointHex, cand := range candidates {
			ls := confirmLastSent[pointHex]
			if ls.IsZero() || time.Since(ls) > confirmResend {
				udpConn.Write([]byte(fmt.Sprintf("MSG %s confirm %x\n", room, cand.confirmPayload)))
				confirmLastSent[pointHex] = time.Now()
			}
		}

		udpConn.SetReadDeadline(time.Now().Add(readDeadline))
		n, _, err := udpConn.ReadFromUDP(readBuf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return "", nil, false, fmt.Errorf("relay read: %w", err)
		}

		line := bytes.TrimSpace(readBuf[:n])

		if bytes.HasPrefix(line, []byte("MSGD ")) {
			now := time.Now()
			if now.Sub(inboundWindow) >= time.Second {
				inboundWindow = now
				inboundCount = 0
			}
			inboundCount++
			if inboundCount > maxInboundMSGDPerSecond {
				continue
			}
		}

		if bytes.HasPrefix(line, []byte("REGD ")) {
			rest := line[len("REGD "):]
			relayRoom, relayPayload, found := bytes.Cut(rest, []byte(" "))
			if string(relayRoom) != room || !found {
				continue
			}
			payload := string(bytes.TrimSpace(relayPayload))
			// "REGD <room> OK <addr>"; confirmed registration.
			if strings.HasPrefix(payload, "OK ") {
				receivedREGD = true
				extAddr := strings.TrimSpace(payload[3:])
				// The relay is untrusted: only accept a printable literal
				// IP:port (never a hostname, which would trigger DNS and allow
				// redirecting the peer's hole-punch/QUIC traffic), and log it
				// quoted so control characters cannot drive the terminal.
				// Otherwise keep the locally detected address.
				if ap, addrErr := netip.ParseAddrPort(extAddr); addrErr == nil && ap.Port() != 0 && printableASCII(extAddr) {
					myAddr = extAddr
					// A relay/same-L2 attacker can repeat REGD OK cheaply;
					// rate-limit the success log so it cannot flood stderr.
					if time.Since(lastExtAddrLog) >= time.Second {
						util.LogRelay.PrintfSuccess("Connected! Ext. address: %q", extAddr)
						lastExtAddrLog = time.Now()
					}
				}
				continue
			}
			// "REGD <room> <cookie>"; cookie challenge (32 hex chars).
			if len(payload) == 32 {
				_, hexErr := hex.DecodeString(payload)
				if hexErr == nil {
					pendingCookie = payload
					lastRegSent = time.Time{} // send cookie-bearing REG immediately
				}
			}
		}

		if bytes.HasPrefix(line, []byte("MSGD spake2 ")) {
			hexBody := string(line[len("MSGD spake2 "):])

			peerPayload, decErr := hex.DecodeString(hexBody)
			if decErr != nil || len(peerPayload) < spake2PayloadLen {
				util.LogSPAKE2.Printf("Invalid SPAKE2 message: short payload")
				continue
			}
			// Deduplicate by the complete payload (M, N, fingerprint), not
			// just M: a fake payload that reuses the real peer's M point but
			// carries an attacker fingerprint must not shadow the genuine
			// retransmissions.
			payloadHex := hex.EncodeToString(peerPayload[:spake2PayloadLen])
			if _, exists := candidates[payloadHex]; exists {
				continue
			}
			effectiveMyPoint, effectivePeerPoint, fp, srv, ck, ek, pErr := processSpakeMsg(hexBody, state, myPointM, myPointN, myFingerprint)
			if pErr != nil {
				util.LogSPAKE2.Printf("Invalid SPAKE2 message: %v", pErr)
				continue
			}
			cp, pErr := buildConfirmPayload(ck, ek, effectiveMyPoint, effectivePeerPoint, myFingerprint, fp, []byte(myAddr))
			if pErr != nil {
				util.LogSPAKE2.Printf("Failed to build confirm payload: %v", pErr)
				spake2.ZeroBytes(ck)
				spake2.ZeroBytes(ek)
				continue
			}

			if len(candidates) >= maxCandidates {
				// Never hard-drop new points, but only evict after the new
				// payload has passed validation, so invalid payloads cannot
				// evict legitimate candidates. Drop the candidate whose
				// confirm was sent longest ago; the real peer retries every
				// 2 s and can re-enter.
				evictKey := ""
				var evictAt time.Time
				for k := range candidates {
					t := confirmLastSent[k]
					if evictKey == "" || t.Before(evictAt) {
						evictKey, evictAt = k, t
					}
				}
				if c, ok := candidates[evictKey]; ok {
					spake2.ZeroBytes(c.confirmKey)
					spake2.ZeroBytes(c.encKey)
				}
				delete(candidates, evictKey)
				delete(confirmLastSent, evictKey)
				util.LogSPAKE2.Printf("SPAKE2 candidate table full (%d), evicting oldest", maxCandidates)
			}
			candidates[payloadHex] = &peerCandidate{
				point: effectivePeerPoint, fingerprint: fp, confirmKey: ck, encKey: ek,
				confirmPayload: cp, isServer: srv, effectiveMyPoint: effectiveMyPoint,
			}
			allKeys = append(allKeys, ck, ek)

			udpConn.Write([]byte(fmt.Sprintf("MSG %s confirm %x\n", room, cp)))
			confirmLastSent[payloadHex] = time.Now()

			util.LogSPAKE2.Printf("Points exchanged with peer")

			for _, hexBody := range bufferedConfirms {
				pa, pErr := processConfirmMsg(hexBody, ck, ek, effectiveMyPoint, effectivePeerPoint, myFingerprint, fp)
				if pErr != nil {
					continue
				}
				util.LogSPAKE2.PrintfSuccess("Peer authenticated (buffered)")
				return pa, fp, srv, nil
			}
		}

		if bytes.HasPrefix(line, []byte("MSGD confirm ")) {
			hexBody := string(line[len("MSGD confirm "):])

			for _, cand := range candidates {
				pa, pErr := processConfirmMsg(hexBody, cand.confirmKey, cand.encKey, cand.effectiveMyPoint, cand.point, myFingerprint, cand.fingerprint)
				if pErr != nil {
					continue
				}
				util.LogSPAKE2.PrintfSuccess("Peer authenticated")
				return pa, cand.fingerprint, cand.isServer, nil
			}

			if len(bufferedConfirms) < maxBufferedConfirms {
				bufferedConfirms = append(bufferedConfirms, hexBody)
			}
		}
	}
}
