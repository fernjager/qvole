package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/fernjager/qvole/internal/util"
	"github.com/fernjager/qvole/spake2"
)

const (
	pointLen              = 65
	spake2PayloadLen      = pointLen*2 + fingerprintSize
	fingerprintSize       = 32
	maxCandidates         = 50
	maxBufferedConfirms   = 200
	readBufferSize        = 1500
	spake2ResendInterval  = 2 * time.Second
	confirmResendInterval = 2 * time.Second
	exchangeReadDeadline  = 1 * time.Second
)

// processSpakeMsg parses the peer's SPAKE2 payload (myPointM || myPointN || fingerprint),
// determines the protocol role by comparing M-points, and selects the effective points:
//
//	Server (larger M-point) uses N; Client uses M.
//
// Returns the effective points, fingerprint, role, and derived session keys.
func processSpakeMsg(body string, state *spake2.State, myPointM, myPointN []byte) (effectiveMyPoint, effectivePeerPoint, peerFingerprint []byte, isServer bool, confirmKey, encKey []byte, err error) {
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

	isServer = bytes.Compare(myPointM, peerPointM) > 0

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
	ck, ek, err := spake2.DeriveSessionKey(shared, effectiveMyPoint, effectivePeerPoint)
	spake2.ZeroBytes(shared)
	if err != nil {
		return nil, nil, nil, false, nil, nil, fmt.Errorf("spake2 derive key: %w", err)
	}
	return effectiveMyPoint, effectivePeerPoint, peerFingerprint, isServer, ck, ek, nil
}

func buildConfirmPayload(confirmKey, encKey, myPoint, peerPoint, peerFingerprint, myAddr []byte) ([]byte, error) {
	aad := spake2.SessionAAD(myPoint, peerPoint)
	confirmNonce := make([]byte, 16)
	if _, err := rand.Read(confirmNonce); err != nil {
		return nil, fmt.Errorf("rand confirm nonce: %w", err)
	}
	confirm, err := spake2.ComputeConfirm(confirmKey, myPoint, peerPoint, confirmNonce, peerFingerprint)
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

func processConfirmMsg(hexBody string, confirmKey, encKey, myPoint, peerPoint, myFingerprint []byte) (peerAddr string, err error) {
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

	if !spake2.VerifyConfirm(confirmKey, myPoint, peerPoint, peerNonce, myFingerprint, peerConfirm) {
		return "", fmt.Errorf("spake2 confirmation mismatch")
	}
	aad := spake2.SessionAAD(myPoint, peerPoint)
	peerAddrBytes, err := spake2.DecryptMetadata(encKey, aad, peerEncAddr)
	if err != nil {
		return "", fmt.Errorf("decrypt peer addr: %w", err)
	}
	return strings.TrimRight(string(peerAddrBytes), "\x00"), nil
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
	receivedREGD := false

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
			udpConn.Write([]byte("REG " + room + "\n"))
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

		if bytes.HasPrefix(line, []byte("REGD ")) {
			rest := line[len("REGD "):]
			relayRoom, relayExtAddr, found := bytes.Cut(rest, []byte(" "))
			if string(relayRoom) == room {
				receivedREGD = true
				if found && len(relayExtAddr) > 0 {
					extAddr := string(bytes.TrimSpace(relayExtAddr))
					if extAddr != "" {
						myAddr = extAddr
						util.LogRelay.PrintfSuccess("Connected! Ext. address: %s", util.Bold(myAddr))
					}
				}
			}
		}

		if bytes.HasPrefix(line, []byte("MSGD spake2 ")) {
			hexBody := string(line[len("MSGD spake2 "):])

			peerPayload, decErr := hex.DecodeString(hexBody)
			if decErr != nil || len(peerPayload) < spake2PayloadLen {
				util.LogSPAKE2.PrintfWarn("Invalid SPAKE2 message: short payload")
				continue
			}
			pointHex := hex.EncodeToString(peerPayload[:pointLen])
			if _, exists := candidates[pointHex]; exists {
				continue
			}
			if len(candidates) >= maxCandidates {
				util.LogSPAKE2.PrintfWarn("Too many SPAKE2 candidates (%d), dropping", len(candidates))
				continue
			}

			effectiveMyPoint, effectivePeerPoint, fp, srv, ck, ek, pErr := processSpakeMsg(hexBody, state, myPointM, myPointN)
			if pErr != nil {
				util.LogSPAKE2.PrintfWarn("Invalid SPAKE2 message: %v", pErr)
				continue
			}
			cp, pErr := buildConfirmPayload(ck, ek, effectiveMyPoint, effectivePeerPoint, fp, []byte(myAddr))
			if pErr != nil {
				util.LogSPAKE2.PrintfError("Failed to build confirm payload: %v", pErr)
				spake2.ZeroBytes(ck)
				spake2.ZeroBytes(ek)
				continue
			}
			candidates[pointHex] = &peerCandidate{
				point: effectivePeerPoint, fingerprint: fp, confirmKey: ck, encKey: ek,
				confirmPayload: cp, isServer: srv, effectiveMyPoint: effectiveMyPoint,
			}
			allKeys = append(allKeys, ck, ek)

			udpConn.Write([]byte(fmt.Sprintf("MSG %s confirm %x\n", room, cp)))
			confirmLastSent[pointHex] = time.Now()

			util.LogSPAKE2.PrintfSuccess("Points exchanged with peer")

			for _, hexBody := range bufferedConfirms {
				pa, pErr := processConfirmMsg(hexBody, ck, ek, effectiveMyPoint, effectivePeerPoint, myFingerprint)
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
				pa, pErr := processConfirmMsg(hexBody, cand.confirmKey, cand.encKey, cand.effectiveMyPoint, cand.point, myFingerprint)
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
