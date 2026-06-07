package qvole

import "time"

// Option configures a qvole connection or operation.
type Option func(*options)

type options struct {
	code              string
	relay             string
	punchTimeout      time.Duration
	exchangeDeadline  time.Duration
	keepAlivePeriod   time.Duration
	idleTimeout       time.Duration
	handshakeTimeout  time.Duration
	maxStreams        int
	forwardMaxStreams int
	command           string
	cmdMode           bool
	localTunnels      []string
	remoteTunnels     []string
	allowTunnel       bool
}

func defaultOptions() *options {
	return &options{
		relay:             "",
		punchTimeout:      10 * time.Second,
		exchangeDeadline:  5 * time.Minute,
		keepAlivePeriod:   2 * time.Second,
		idleTimeout:       2 * time.Minute,
		handshakeTimeout:  30 * time.Second,
		maxStreams:        100,
		forwardMaxStreams: 200,
	}
}

// WithCode sets the shared secret code for peer authentication.
func WithCode(code string) Option {
	return func(o *options) { o.code = code }
}

// WithRelay sets the relay server address (host:port).
func WithRelay(addr string) Option {
	return func(o *options) { o.relay = addr }
}

// WithPunchTimeout sets the maximum duration for UDP hole punching.
func WithPunchTimeout(d time.Duration) Option {
	return func(o *options) { o.punchTimeout = d }
}

// WithExchangeDeadline sets the maximum duration for the SPAKE2 exchange.
func WithExchangeDeadline(d time.Duration) Option {
	return func(o *options) { o.exchangeDeadline = d }
}

// WithKeepAlive sets the QUIC keepalive interval.
func WithKeepAlive(d time.Duration) Option {
	return func(o *options) { o.keepAlivePeriod = d }
}

// WithIdleTimeout sets the QUIC idle timeout before the connection is closed.
func WithIdleTimeout(d time.Duration) Option {
	return func(o *options) { o.idleTimeout = d }
}

// WithHandshakeTimeout sets the maximum duration for the QUIC handshake.
func WithHandshakeTimeout(d time.Duration) Option {
	return func(o *options) { o.handshakeTimeout = d }
}

// WithMaxStreams sets the maximum number of incoming bidirectional streams
// the peer is allowed to open.
func WithMaxStreams(n int) Option {
	return func(o *options) { o.maxStreams = n }
}

// WithForwardMaxStreams sets the maximum number of incoming bidirectional
// streams that tunnel forwarding is allowed to open.
func WithForwardMaxStreams(n int) Option {
	return func(o *options) { o.forwardMaxStreams = n }
}

// WithCommand sets the command to run in exec mode.
func WithCommand(cmd string) Option {
	return func(o *options) { o.command = cmd }
}

// WithCmdMode sets whether this side runs the command (true) or bridges
// stdin/stdout to the peer's command (false).
func WithCmdMode(b bool) Option {
	return func(o *options) { o.cmdMode = b }
}

// WithLocalTunnel adds a local port-forwarding tunnel specification
// in the format [addr:]port:host:port.
func WithLocalTunnel(spec string) Option {
	return func(o *options) { o.localTunnels = append(o.localTunnels, spec) }
}

// WithRemoteTunnel adds a remote port-forwarding tunnel specification
// in the format [addr:]port:host:port.
func WithRemoteTunnel(spec string) Option {
	return func(o *options) { o.remoteTunnels = append(o.remoteTunnels, spec) }
}

// WithAllowTunnel sets whether tunnel requests from the peer are accepted.
func WithAllowTunnel(b bool) Option {
	return func(o *options) { o.allowTunnel = b }
}
