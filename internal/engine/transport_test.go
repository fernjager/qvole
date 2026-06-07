package engine

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/util"
)

func TestEnvDuration_Default(t *testing.T) {
	os.Unsetenv("QVOLE_TEST_DUR")
	d := util.EnvDuration("QVOLE_TEST_DUR", 5*time.Second)
	if d != 5*time.Second {
		t.Fatalf("expected 5s, got %v", d)
	}
}

func TestEnvDuration_Valid(t *testing.T) {
	os.Setenv("QVOLE_TEST_DUR", "3000")
	defer os.Unsetenv("QVOLE_TEST_DUR")
	d := util.EnvDuration("QVOLE_TEST_DUR", 5*time.Second)
	if d != 3*time.Second {
		t.Fatalf("expected 3s, got %v", d)
	}
}

func TestEnvDuration_Invalid(t *testing.T) {
	os.Setenv("QVOLE_TEST_DUR", "not-a-number")
	defer os.Unsetenv("QVOLE_TEST_DUR")
	d := util.EnvDuration("QVOLE_TEST_DUR", 5*time.Second)
	if d != 5*time.Second {
		t.Fatalf("expected default 5s, got %v", d)
	}
}

func TestEnvDuration_Zero(t *testing.T) {
	os.Setenv("QVOLE_TEST_DUR", "0")
	defer os.Unsetenv("QVOLE_TEST_DUR")
	d := util.EnvDuration("QVOLE_TEST_DUR", 5*time.Second)
	if d != 5*time.Second {
		t.Fatalf("expected default 5s for zero, got %v", d)
	}
}

func TestEnvDuration_Negative(t *testing.T) {
	os.Setenv("QVOLE_TEST_DUR", "-100")
	defer os.Unsetenv("QVOLE_TEST_DUR")
	d := util.EnvDuration("QVOLE_TEST_DUR", 5*time.Second)
	if d != 5*time.Second {
		t.Fatalf("expected default 5s for negative, got %v", d)
	}
}

func TestEnvInt_Default(t *testing.T) {
	os.Unsetenv("QVOLE_TEST_INT")
	n := util.EnvInt("QVOLE_TEST_INT", 42)
	if n != 42 {
		t.Fatalf("expected 42, got %d", n)
	}
}

func TestEnvInt_Valid(t *testing.T) {
	os.Setenv("QVOLE_TEST_INT", "100")
	defer os.Unsetenv("QVOLE_TEST_INT")
	n := util.EnvInt("QVOLE_TEST_INT", 42)
	if n != 100 {
		t.Fatalf("expected 100, got %d", n)
	}
}

func TestEnvInt_Invalid(t *testing.T) {
	os.Setenv("QVOLE_TEST_INT", "not-a-number")
	defer os.Unsetenv("QVOLE_TEST_INT")
	n := util.EnvInt("QVOLE_TEST_INT", 42)
	if n != 42 {
		t.Fatalf("expected default 42, got %d", n)
	}
}

func TestEnvInt_Zero(t *testing.T) {
	os.Setenv("QVOLE_TEST_INT", "0")
	defer os.Unsetenv("QVOLE_TEST_INT")
	n := util.EnvInt("QVOLE_TEST_INT", 42)
	if n != 42 {
		t.Fatalf("expected default 42 for zero, got %d", n)
	}
}

func TestEnvInt_Negative(t *testing.T) {
	os.Setenv("QVOLE_TEST_INT", "-5")
	defer os.Unsetenv("QVOLE_TEST_INT")
	n := util.EnvInt("QVOLE_TEST_INT", 42)
	if n != 42 {
		t.Fatalf("expected default 42 for negative, got %d", n)
	}
}

func TestBaseTLSConfig_MinVersion(t *testing.T) {
	cert, err := util.GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("util.GenerateSelfSignedCert: %v", err)
	}
	fp := util.CertFingerprint(cert)
	cfg := baseTLSConfig(cert, fp, 0)
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected TLS 1.3 (%d), got %d", tls.VersionTLS13, cfg.MinVersion)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("expected InsecureSkipVerify = true")
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "qvole-v0.1" {
		t.Fatalf("expected NextProtos [\"qvole-v0.1\"], got %v", cfg.NextProtos)
	}
}

func TestBaseTLSConfig_EmptyCerts(t *testing.T) {
	cert, err := util.GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("util.GenerateSelfSignedCert: %v", err)
	}
	fp := util.CertFingerprint(cert)
	cfg := baseTLSConfig(cert, fp, 0)

	err = cfg.VerifyPeerCertificate([][]byte{}, nil)
	if err == nil || err.Error() != "no peer certificate" {
		t.Fatalf("expected 'no peer certificate', got %v", err)
	}
}

func TestBaseTLSConfig_WrongFingerprint(t *testing.T) {
	cert, err := util.GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("util.GenerateSelfSignedCert: %v", err)
	}
	wrongFP := make([]byte, 32)
	wrongFP[0] = 0x01

	cfg := baseTLSConfig(cert, wrongFP, 0)

	// Create a different cert to get different DER bytes
	cert2, err := util.GenerateSelfSignedCert("other")
	if err != nil {
		t.Fatalf("util.GenerateSelfSignedCert: %v", err)
	}

	err = cfg.VerifyPeerCertificate(cert2.Certificate, nil)
	if err == nil || err.Error() != "peer certificate fingerprint mismatch" {
		t.Fatalf("expected 'peer certificate fingerprint mismatch', got %v", err)
	}
}

func TestBaseTLSConfig_MatchingFingerprint(t *testing.T) {
	cert, err := util.GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("util.GenerateSelfSignedCert: %v", err)
	}
	fp := util.CertFingerprint(cert)

	cfg := baseTLSConfig(cert, fp, 0)

	err = cfg.VerifyPeerCertificate(cert.Certificate, nil)
	if err != nil {
		t.Fatalf("expected no error for matching fingerprint, got %v", err)
	}
}

func TestBaseTLSConfig_CertAuthPipeline(t *testing.T) {
	// End-to-end: generate cert on "server" side, compute fingerprint,
	// create TLS config for "client" that pins it, verify it accepts.
	serverCert, err := util.GenerateSelfSignedCert("server")
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	serverFP := util.CertFingerprint(serverCert)

	clientCfg := baseTLSConfig(serverCert, serverFP, 0)
	if err = clientCfg.VerifyPeerCertificate(serverCert.Certificate, nil); err != nil {
		t.Fatalf("client should accept server cert: %v", err)
	}

	// Different cert should fail
	otherCert, err := util.GenerateSelfSignedCert("other")
	if err != nil {
		t.Fatalf("other cert: %v", err)
	}
	if err := clientCfg.VerifyPeerCertificate(otherCert.Certificate, nil); err == nil {
		t.Fatal("client should reject different cert")
	}
}

func TestServerClientTLSConfig(t *testing.T) {
	cert, err := util.GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("util.GenerateSelfSignedCert: %v", err)
	}
	fp := util.CertFingerprint(cert)

	serverCfg := ServerTLSConfig(cert, fp)
	if serverCfg.ClientAuth != tls.RequireAnyClientCert {
		t.Fatalf("expected RequireAnyClientCert (%d), got %d", tls.RequireAnyClientCert, serverCfg.ClientAuth)
	}

	clientCfg := ClientTLSConfig(cert, fp)
	if clientCfg.ClientAuth != tls.NoClientCert {
		t.Fatalf("expected NoClientCert (%d), got %d", tls.NoClientCert, clientCfg.ClientAuth)
	}
}

func TestQUICConfig_Defaults(t *testing.T) {
	os.Unsetenv("QVOLE_MAX_STREAMS")
	os.Unsetenv("QVOLE_KEEPALIVE_MS")
	os.Unsetenv("QVOLE_IDLE_TIMEOUT_MS")
	os.Unsetenv("QVOLE_HANDSHAKE_TIMEOUT_MS")

	cfg := QUICConfig()
	if cfg.MaxIncomingStreams != 100 {
		t.Fatalf("expected 100 MaxIncomingStreams, got %d", cfg.MaxIncomingStreams)
	}
	if cfg.MaxIncomingUniStreams != 0 {
		t.Fatalf("expected 0 MaxIncomingUniStreams, got %d", cfg.MaxIncomingUniStreams)
	}
	if cfg.KeepAlivePeriod != 2*time.Second {
		t.Fatalf("expected 2s KeepAlivePeriod, got %v", cfg.KeepAlivePeriod)
	}
	if cfg.MaxIdleTimeout != 2*time.Minute {
		t.Fatalf("expected 2m MaxIdleTimeout, got %v", cfg.MaxIdleTimeout)
	}
	if cfg.HandshakeIdleTimeout != 30*time.Second {
		t.Fatalf("expected 30s HandshakeIdleTimeout, got %v", cfg.HandshakeIdleTimeout)
	}
}

func TestQUICConfig_EnvOverrides(t *testing.T) {
	os.Setenv("QVOLE_MAX_STREAMS", "500")
	os.Setenv("QVOLE_KEEPALIVE_MS", "5000")
	os.Setenv("QVOLE_IDLE_TIMEOUT_MS", "60000")
	os.Setenv("QVOLE_HANDSHAKE_TIMEOUT_MS", "15000")
	defer func() {
		os.Unsetenv("QVOLE_MAX_STREAMS")
		os.Unsetenv("QVOLE_KEEPALIVE_MS")
		os.Unsetenv("QVOLE_IDLE_TIMEOUT_MS")
		os.Unsetenv("QVOLE_HANDSHAKE_TIMEOUT_MS")
	}()

	cfg := QUICConfig()
	if cfg.MaxIncomingStreams != 500 {
		t.Fatalf("expected 500 MaxIncomingStreams, got %d", cfg.MaxIncomingStreams)
	}
	if cfg.KeepAlivePeriod != 5*time.Second {
		t.Fatalf("expected 5s KeepAlivePeriod, got %v", cfg.KeepAlivePeriod)
	}
	if cfg.MaxIdleTimeout != 60*time.Second {
		t.Fatalf("expected 60s MaxIdleTimeout, got %v", cfg.MaxIdleTimeout)
	}
	if cfg.HandshakeIdleTimeout != 15*time.Second {
		t.Fatalf("expected 15s HandshakeIdleTimeout, got %v", cfg.HandshakeIdleTimeout)
	}
}

func TestQUICConfig_WindowSizes(t *testing.T) {
	cfg := QUICConfig()
	if cfg.InitialStreamReceiveWindow != 1*1024*1024 {
		t.Fatalf("expected 1MB stream window, got %d", cfg.InitialStreamReceiveWindow)
	}
	if cfg.InitialConnectionReceiveWindow != 4*1024*1024 {
		t.Fatalf("expected 4MB conn window, got %d", cfg.InitialConnectionReceiveWindow)
	}
}

func TestGetForwardMaxStreams_Default(t *testing.T) {
	os.Unsetenv("QVOLE_FORWARD_MAX_STREAMS")
	n := GetForwardMaxStreams()
	if n != 200 {
		t.Fatalf("expected 200, got %d", n)
	}
}

func TestGetForwardMaxStreams_Env(t *testing.T) {
	os.Setenv("QVOLE_FORWARD_MAX_STREAMS", "50")
	defer os.Unsetenv("QVOLE_FORWARD_MAX_STREAMS")
	n := GetForwardMaxStreams()
	if n != 50 {
		t.Fatalf("expected 50, got %d", n)
	}
}

func TestGetForwardMaxStreams_Invalid(t *testing.T) {
	os.Setenv("QVOLE_FORWARD_MAX_STREAMS", "invalid")
	defer os.Unsetenv("QVOLE_FORWARD_MAX_STREAMS")
	n := GetForwardMaxStreams()
	if n != 200 {
		t.Fatalf("expected default 200 for invalid, got %d", n)
	}
}

func TestGetForwardMaxStreams_Zero(t *testing.T) {
	os.Setenv("QVOLE_FORWARD_MAX_STREAMS", "0")
	defer os.Unsetenv("QVOLE_FORWARD_MAX_STREAMS")
	n := GetForwardMaxStreams()
	if n != 200 {
		t.Fatalf("expected default 200 for zero, got %d", n)
	}
}

func TestCloseOnCancel(t *testing.T) {
	cert, err := util.GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("util.GenerateSelfSignedCert: %v", err)
	}
	fp := util.CertFingerprint(cert)

	lnUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer lnUDP.Close()

	ln, err := quic.Listen(lnUDP, ServerTLSConfig(cert, fp), QUICConfig())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())

	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept(ctx)
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- conn.Context().Err()
	}()

	clientUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client udp: %v", err)
	}
	defer clientUDP.Close()

	clientConn, err := quic.Dial(
		context.Background(),
		clientUDP,
		ln.Addr().(*net.UDPAddr),
		ClientTLSConfig(cert, fp),
		QUICConfig(),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	closeOnCancel(ctx, clientConn)

	cancel()

	select {
	case <-clientConn.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("connection not closed after context cancel")
	}

	clientConn.CloseWithError(0, "test")
}
