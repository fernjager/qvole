package app

import (
	"context"
	"net"
	"testing"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
)

func setupQUICPair(t *testing.T) (clientConn *quic.Conn, serverConn *quic.Conn) {
	t.Helper()
	cert, err := util.GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	fp := util.CertFingerprint(cert)

	lnUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { lnUDP.Close() })

	ln, err := quic.Listen(lnUDP, engine.ServerTLSConfig(cert, fp), engine.QUICConfig())
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	type result struct {
		conn *quic.Conn
		err  error
	}
	serverCh := make(chan result, 1)
	go func() {
		conn, err := ln.Accept(context.Background())
		serverCh <- result{conn, err}
	}()

	clientUDP, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client udp: %v", err)
	}
	t.Cleanup(func() { clientUDP.Close() })

	clientConn, err = quic.Dial(
		context.Background(),
		clientUDP,
		ln.Addr().(*net.UDPAddr),
		engine.ClientTLSConfig(cert, fp),
		engine.QUICConfig(),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	sr := <-serverCh
	if sr.err != nil {
		t.Fatalf("accept: %v", sr.err)
	}
	serverConn = sr.conn

	t.Cleanup(func() {
		clientConn.CloseWithError(0, "test")
		serverConn.CloseWithError(0, "test")
	})
	return clientConn, serverConn
}
