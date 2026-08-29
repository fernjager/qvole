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
	streamIdleTimeout time.Duration
	command           string
	cmdMode           bool
	localTunnels      []string
	remoteTunnels     []string
	allowAll          bool
	allowListen       []string
	allowForward      []string
}

func defaultOptions() *options {
	return &options{
		relay:             "",
		punchTimeout:      10 * time.Second,
		exchangeDeadline:  5 * time.Minute,
		keepAlivePeriod:   5 * time.Second,
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

// WithStreamIdleTimeout sets the inactivity window before a silent forwarded
// tunnel stream is reaped. A value of 0 disables the per-stream cap (the
// default), so forwarded connections rely on the connection-level QUIC
// keepalive/idle timeouts and are never force-closed. When > 0 the deadline is
// refreshed on data flowing in either direction, so only genuinely silent
// streams are reaped. Overrides the QVOLE_STREAM_IDLE_TIMEOUT_MS env var.
func WithStreamIdleTimeout(d time.Duration) Option {
	return func(o *options) { o.streamIdleTimeout = d }
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
// in the format [laddr:]lport:raddr:rport.
func WithLocalTunnel(spec string) Option {
	return func(o *options) { o.localTunnels = append(o.localTunnels, spec) }
}

// WithRemoteTunnel adds a remote port-forwarding tunnel specification
// in the format [raddr:]rport:laddr:lport.
func WithRemoteTunnel(spec string) Option {
	return func(o *options) { o.remoteTunnels = append(o.remoteTunnels, spec) }
}

// WithAllowAll accepts every peer tunnel request without matching. This is the
// "allow anything" mode (not safe): the peer may open arbitrary listening ports
// and reach arbitrary addresses through this machine. Prefer WithAllowListen /
// WithAllowForward.
func WithAllowAll(b bool) Option {
	return func(o *options) { o.allowAll = b }
}

// WithAllowListen allowlists an addr:port on which the peer may ask this side
// to open a listening port (honors the peer's -R request for that address).
// Repeatable. Without any allowlist entries (or WithAllowAll), no peer tunnel
// requests are accepted.
func WithAllowListen(addrPort string) Option {
	return func(o *options) { o.allowListen = append(o.allowListen, addrPort) }
}

// WithAllowForward allowlists an addr:port that the peer may reach through
// this side (honors the peer's -L request targeting that address). Repeatable.
// Without any allowlist entries (or WithAllowAll), no peer tunnel requests are
// accepted.
func WithAllowForward(addrPort string) Option {
	return func(o *options) { o.allowForward = append(o.allowForward, addrPort) }
}
