package qvole

import (
	"context"
	"errors"
	"net"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/app"
	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
)

// ProtocolVersion is the wire protocol version string (e.g. "0.1").
const ProtocolVersion = engine.ProtocolVersion

// BufferPool is a sync.Pool of 32 KB reusable byte buffers.
var BufferPool = engine.BufferPool

// CloseWrite signals the peer that no more data will be written, while keeping
// the connection open for reading. This is equivalent to a half-close (FIN).
// After CloseWrite, the peer's Read will return io.EOF once all prior data has
// been consumed. The caller must still call Close to release resources.
func CloseWrite(conn net.Conn) error {
	if c, ok := conn.(interface{ CloseWrite() error }); ok {
		return c.CloseWrite()
	}
	return conn.Close()
}

// PutBuffer zeroes and returns a buffer to the pool.
func PutBuffer(buf []byte) { engine.PutBuffer(buf) }

// toPeerConfig converts library options to an engine PeerConfig.
func toPeerConfig(o *options) engine.PeerConfig {
	return engine.PeerConfig{
		PunchTimeout:     o.punchTimeout,
		ExchangeDeadline: o.exchangeDeadline,
		KeepAlivePeriod:  o.keepAlivePeriod,
		IdleTimeout:      o.idleTimeout,
		HandshakeTimeout: o.handshakeTimeout,
		MaxStreams:       o.maxStreams,
	}
}

func resolveOptions(opts []Option) *options {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	return o
}

func validate(o *options) error {
	if o.code == "" {
		return errors.New("code is required")
	}
	if o.relay == "" {
		return errors.New("relay is required")
	}
	return nil
}

// Dial establishes a secure P2P connection and opens a single bidirectional
// QUIC stream, returning it as a net.Conn. The peer must call Accept with the
// same code. Code and relay are required.
func Dial(ctx context.Context, opts ...Option) (net.Conn, error) {
	o := resolveOptions(opts)
	if err := validate(o); err != nil {
		return nil, err
	}
	conn, _, err := engine.ConnectPeerWithConfig(ctx, o.relay, o.code, toPeerConfig(o))
	if err != nil {
		return nil, err
	}
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		conn.CloseWithError(0, "")
		return nil, err
	}
	return &quicStreamConn{
		stream: stream,
		conn:   conn,
		laddr:  conn.LocalAddr(),
		raddr:  conn.RemoteAddr(),
	}, nil
}

// Accept establishes a secure P2P connection and waits for the peer's stream,
// returning it as a net.Conn. The peer must call Dial with the same code.
// Code is required.
func Accept(ctx context.Context, opts ...Option) (net.Conn, error) {
	o := resolveOptions(opts)
	if err := validate(o); err != nil {
		return nil, err
	}
	conn, _, err := engine.ConnectPeerWithConfig(ctx, o.relay, o.code, toPeerConfig(o))
	if err != nil {
		return nil, err
	}
	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		conn.CloseWithError(0, "")
		return nil, err
	}
	return &quicStreamConn{
		stream: stream,
		conn:   conn,
		laddr:  conn.LocalAddr(),
		raddr:  conn.RemoteAddr(),
	}, nil
}

// Connect establishes a secure P2P QUIC connection authenticated by a shared
// code. Returns the raw quic.Conn and whether this peer is the server. Use
// this if you need multiple streams. Code is required.
func Connect(ctx context.Context, opts ...Option) (*quic.Conn, bool, error) {
	o := resolveOptions(opts)
	if err := validate(o); err != nil {
		return nil, false, err
	}
	return engine.ConnectPeerWithConfig(ctx, o.relay, o.code, toPeerConfig(o))
}

// Exec connects to a peer and either runs a command locally (cmdMode=true) or
// bridges stdin/stdout to the peer's command (cmdMode=false). Code is required.
// When cmdMode is true, WithCommand must also be set.
func Exec(ctx context.Context, opts ...Option) error {
	o := resolveOptions(opts)
	if err := validate(o); err != nil {
		return err
	}
	return app.RunExec(ctx, o.relay, o.code, o.command, o.cmdMode)
}

// Tunnel establishes port-forwarding tunnels between peers. Use WithLocalTunnel
// and WithRemoteTunnel to specify forwarding rules. Code is required.
func Tunnel(ctx context.Context, opts ...Option) error {
	o := resolveOptions(opts)
	if err := validate(o); err != nil {
		return err
	}
	return app.RunTunnel(ctx, o.relay, o.code, o.localTunnels, o.remoteTunnels, o.allowTunnel)
}

// TunnelRequest represents a single port-forwarding tunnel specification.
type TunnelRequest = app.TunnelRequest

// ParseTunnelRequest parses a tunnel spec string into a TunnelRequest.
func ParseTunnelRequest(spec, typ string) (*TunnelRequest, error) {
	return app.ParseTunnelRequest(spec, typ)
}

// GenerateCode generates a random human-readable code like "0000-word-word-word".
func GenerateCode() (string, error) {
	return util.GenerateCode()
}

// Nameplate derives a 4-character room identifier from a code.
func Nameplate(code string) string {
	return util.Nameplate(code)
}
