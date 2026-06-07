package engine

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/util"
)

const (
	ProtocolVersion                       = "0.1"
	defaultKeepAlivePeriod                = 2 * time.Second
	defaultMaxIdleTimeout                 = 2 * time.Minute
	defaultHandshakeIdleTimeout           = 30 * time.Second
	defaultMaxIncomingStreams             = 100
	defaultForwardMaxStreams              = 200
	defaultInitialStreamWindow            = 1 * 1024 * 1024
	defaultInitialConnectionReceiveWindow = 4 * 1024 * 1024
	alpnProtocol                          = "qvole-v" + ProtocolVersion
	cancelErrorCode                       = 1
)

func baseTLSConfig(cert tls.Certificate, peerFingerprint []byte, clientAuth tls.ClientAuthType) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
		ClientAuth:         clientAuth,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("no peer certificate")
			}
			h := sha256.Sum256(rawCerts[0])
			if subtle.ConstantTimeCompare(h[:], peerFingerprint) != 1 {
				return fmt.Errorf("peer certificate fingerprint mismatch")
			}
			return nil
		},
		NextProtos: []string{alpnProtocol},
	}
}

// ServerTLSConfig returns a TLS 1.3 server config that pins the peer certificate
// to the given SHA-256 fingerprint using VerifyPeerCertificate.
func ServerTLSConfig(cert tls.Certificate, peerFingerprint []byte) *tls.Config {
	return baseTLSConfig(cert, peerFingerprint, tls.RequireAnyClientCert)
}

// ClientTLSConfig returns a TLS 1.3 client config that pins the peer certificate
// to the given SHA-256 fingerprint using VerifyPeerCertificate.
func ClientTLSConfig(cert tls.Certificate, peerFingerprint []byte) *tls.Config {
	return baseTLSConfig(cert, peerFingerprint, tls.NoClientCert)
}

// QUICConfig returns a quic.Config tuned for bulk data transfer, with max streams,
// keepalive, and idle/handshake timeouts overridden from environment variables.
func QUICConfig() *quic.Config {
	return QUICConfigWithOverrides(PeerConfig{})
}

// QUICConfigWithOverrides returns a quic.Config with values taken from cfg when
// non-zero, falling back to environment variables or built-in defaults.
func QUICConfigWithOverrides(cfg PeerConfig) *quic.Config {
	return &quic.Config{
		MaxIncomingStreams:             int64(resolveInt(cfg.MaxStreams, "QVOLE_MAX_STREAMS", defaultMaxIncomingStreams)),
		MaxIncomingUniStreams:          0,
		KeepAlivePeriod:                resolveDur(cfg.KeepAlivePeriod, "QVOLE_KEEPALIVE_MS", defaultKeepAlivePeriod),
		MaxIdleTimeout:                 resolveDur(cfg.IdleTimeout, "QVOLE_IDLE_TIMEOUT_MS", defaultMaxIdleTimeout),
		HandshakeIdleTimeout:           resolveDur(cfg.HandshakeTimeout, "QVOLE_HANDSHAKE_TIMEOUT_MS", defaultHandshakeIdleTimeout),
		InitialStreamReceiveWindow:     uint64(util.EnvInt("QVOLE_INITIAL_STREAM_WINDOW", defaultInitialStreamWindow)),
		InitialConnectionReceiveWindow: uint64(util.EnvInt("QVOLE_INITIAL_CONNECTION_WINDOW", defaultInitialConnectionReceiveWindow)),
	}
}

func resolveInt(val int, envName string, def int) int {
	if val > 0 {
		return val
	}
	return util.EnvInt(envName, def)
}

// GetForwardMaxStreams returns the maximum number of concurrent forward streams,
// configurable via QVOLE_FORWARD_MAX_STREAMS.
func GetForwardMaxStreams() int {
	return util.EnvInt("QVOLE_FORWARD_MAX_STREAMS", defaultForwardMaxStreams)
}

func closeOnCancel(ctx context.Context, conn *quic.Conn) {
	go func() {
		select {
		case <-ctx.Done():
			conn.CloseWithError(cancelErrorCode, "canceled")
		case <-conn.Context().Done():
		}
	}()
}
