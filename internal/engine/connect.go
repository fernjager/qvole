package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/util"
)

// PeerConfig holds optional overrides for connection parameters.
// Zero-value fields fall back to environment variables or built-in defaults.
type PeerConfig struct {
	PunchTimeout     time.Duration
	ExchangeDeadline time.Duration
	Spake2Resend     time.Duration
	ConfirmResend    time.Duration
	ReadDeadline     time.Duration
	RegInterval      time.Duration
	MaxStreams       int
	KeepAlivePeriod  time.Duration
	IdleTimeout      time.Duration
	HandshakeTimeout time.Duration
}

func (c PeerConfig) punchTimeout() time.Duration {
	return resolveDur(c.PunchTimeout, "QVOLE_PUNCH_TIMEOUT_MS", defaultPunchTimeout)
}
func (c PeerConfig) exchangeDeadline() time.Duration {
	return resolveDur(c.ExchangeDeadline, "QVOLE_EXCHANGE_DEADLINE_MS", ExchangeDeadline)
}
func (c PeerConfig) spake2Resend() time.Duration {
	return resolveDur(c.Spake2Resend, "QVOLE_SPAKE2_RESEND_MS", spake2ResendInterval)
}
func (c PeerConfig) confirmResend() time.Duration {
	return resolveDur(c.ConfirmResend, "QVOLE_CONFIRM_RESEND_MS", confirmResendInterval)
}
func (c PeerConfig) readDeadline() time.Duration {
	return resolveDur(c.ReadDeadline, "QVOLE_EXCHANGE_READ_DEADLINE_MS", exchangeReadDeadline)
}
func (c PeerConfig) regInterval() time.Duration {
	return resolveDur(c.RegInterval, "QVOLE_REG_INTERVAL_MS", RegInterval)
}

func resolveDur(val time.Duration, envName string, def time.Duration) time.Duration {
	if val > 0 {
		return val
	}
	return util.EnvDuration(envName, def)
}

// ConnectPeer establishes a peer-to-peer QUIC connection via SPAKE2 PAKE exchange.
// The SPAKE2 shared secret Z = x*y*G uses fresh ephemeral scalars (x, y) per session,
// providing forward secrecy for metadata (peer address, cert fingerprint). Compromising
// the password later does not reveal past metadata; CDH prevents deriving x*y*G from
// recorded blinded points without the ephemeral private keys.
func ConnectPeer(ctx context.Context, relayAddr string, code string) (*quic.Conn, bool, error) {
	return ConnectPeerWithConfig(ctx, relayAddr, code, PeerConfig{})
}

// ConnectPeerWithConfig is like ConnectPeer but accepts a PeerConfig for tuning.
func ConnectPeerWithConfig(ctx context.Context, relayAddr string, code string, cfg PeerConfig) (*quic.Conn, bool, error) {
	generate := code == ""
	if generate {
		var err error
		code, err = util.GenerateCode()
		if err != nil {
			return nil, false, fmt.Errorf("generate code: %w", err)
		}
		fmt.Fprintf(os.Stderr, codeOutputFmt, code)
	}

	room := util.Nameplate(code)

	myCert, err := util.GenerateSelfSignedCert(certCommonName)
	if err != nil {
		return nil, false, fmt.Errorf("generate cert: %w", err)
	}
	myFingerprint := util.CertFingerprint(myCert)

	relayUDPAddr, err := net.ResolveUDPAddr("udp", relayAddr)
	if err != nil {
		return nil, false, fmt.Errorf("resolve relay: %w", err)
	}

	// Use a connected UDP socket for the exchange phase so the kernel
	// filters spoofed packets from non-relay sources on shared networks.
	exchangeConn, err := net.DialUDP("udp", nil, relayUDPAddr)
	if err != nil {
		return nil, false, fmt.Errorf("dial relay: %w", err)
	}

	peerAddrStr, peerFingerprint, isServer, err := registerAndExchange(ctx, exchangeConn, room, code, myFingerprint, cfg)
	localAddr, ok := exchangeConn.LocalAddr().(*net.UDPAddr)
	exchangeConn.Close()
	if !ok {
		return nil, false, fmt.Errorf("expected *net.UDPAddr, got %T", exchangeConn.LocalAddr())
	}

	if err != nil {
		return nil, false, fmt.Errorf("exchange: %w", err)
	}

	// Re-bind a wildcard socket on the same local port for hole punching
	// and QUIC. SO_REUSEADDR avoids EADDRINUSE from the just-closed socket.
	lc := &net.ListenConfig{
		Control: rebindControl,
	}
	pc, err := lc.ListenPacket(ctx, "udp", localAddr.String())
	if err != nil {
		return nil, false, fmt.Errorf("re-bind for QUIC: %w", err)
	}
	udpConn, ok := pc.(*net.UDPConn)
	if !ok {
		pc.Close()
		return nil, false, fmt.Errorf("expected *net.UDPConn, got %T", pc)
	}

	peerUDPAddr, err := net.ResolveUDPAddr("udp", peerAddrStr)
	if err != nil {
		udpConn.Close()
		return nil, false, fmt.Errorf("resolve peer: %w", err)
	}
	if peerUDPAddr.IP == nil || peerUDPAddr.Port == 0 {
		udpConn.Close()
		return nil, false, fmt.Errorf("invalid peer address: %s", peerAddrStr)
	}

	role := util.RoleString(isServer)
	util.LogHole.PrintfInfo("Hole punching to %s as %s", util.Bold(peerUDPAddr.String()), role)

	punchTimeout := cfg.punchTimeout()
	punchCtx, cancel := context.WithTimeout(ctx, punchTimeout)
	successCh := startHolePunching(punchCtx, udpConn, peerUDPAddr)
	select {
	case observedAddr := <-successCh:
		util.LogHole.PrintfSuccess("Hole punch succeeded")
		if observedAddr.IP.Equal(peerUDPAddr.IP) && observedAddr.Port != peerUDPAddr.Port {
			util.LogHole.PrintfInfo("Peer port corrected: %d -> %d", peerUDPAddr.Port, observedAddr.Port)
			peerUDPAddr = observedAddr
		}
	case <-punchCtx.Done():
		util.LogHole.PrintfWarn("Hole punch timed out, falling back to QUIC")
	}
	cancel()
	<-successCh // wait for hole punch goroutines to observe cancellation before clearing deadline
	udpConn.SetReadDeadline(time.Time{})

	helperCtx, helperCancel := context.WithTimeout(context.Background(), helperTimeout)
	go func() {
		defer helperCancel()
		t := time.NewTicker(helperTickerInterval)
		defer t.Stop()
		for {
			select {
			case <-helperCtx.Done():
				return
			case <-t.C:
				udpConn.WriteTo(keepalivePayload, peerUDPAddr)
			}
		}
	}()

	var conn *quic.Conn
	var ln *quic.Listener
	if isServer {
		ln, err = quic.Listen(udpConn, ServerTLSConfig(myCert, peerFingerprint), QUICConfigWithOverrides(cfg))
		if err != nil {
			udpConn.Close()
			util.LogHole.PrintfError("QUIC listen failed: %v", err)
			return nil, false, fmt.Errorf("listen: %w", err)
		}
		defer ln.Close()
		conn, err = ln.Accept(ctx)
		if err != nil {
			udpConn.Close()
			util.LogHole.PrintfError("QUIC accept failed: %v", err)
			return nil, false, fmt.Errorf("accept: %w", err)
		}
	} else {
		conn, err = quic.Dial(ctx, udpConn, peerUDPAddr, ClientTLSConfig(myCert, peerFingerprint), QUICConfigWithOverrides(cfg))
		if err != nil {
			udpConn.Close()
			util.LogHole.PrintfError("QUIC dial to %s failed: %v", peerUDPAddr, err)
			return nil, false, fmt.Errorf("dial: %w", err)
		}
	}

	closeOnCancel(ctx, conn)

	return conn, isServer, nil
}

const (
	ExchangeDeadline     = 5 * time.Minute
	RegInterval          = 60 * time.Second
	MaxMetadataSize      = 52
	confirmNonceSize     = 16
	confirmHMACSize      = 32
	EncryptedAddrSize    = 12 + MaxMetadataSize + 16
	ConfirmMinSize       = confirmNonceSize + confirmHMACSize + EncryptedAddrSize
	confirmRandPad       = 32
	ConfirmPayloadSize   = ConfirmMinSize + confirmRandPad
	defaultPunchTimeout  = 10 * time.Second
	helperTimeout        = 3 * time.Second
	helperTickerInterval = 50 * time.Millisecond
	codeOutputFmt        = "QVOLE_CODE=%s\n"
	certCommonName       = "qvole"
)

var keepalivePayload = []byte{0x01}
