package qvole

import (
	"testing"
	"time"

	"github.com/fernjager/qvole/internal/engine"
)

func TestDefaultOptions(t *testing.T) {
	o := defaultOptions()
	if o.relay != "" {
		t.Errorf("relay should be empty by default, got %q", o.relay)
	}
	// Fields with an engine-level env-var fallback default to zero so the
	// engine's env → built-in-default resolution applies (see PeerConfig).
	if o.punchTimeout != 0 {
		t.Errorf("punchTimeout = %v, want 0 (engine env/default)", o.punchTimeout)
	}
	if o.exchangeDeadline != 0 {
		t.Errorf("exchangeDeadline = %v, want 0 (engine env/default)", o.exchangeDeadline)
	}
	if o.keepAlivePeriod != 0 {
		t.Errorf("keepAlivePeriod = %v, want 0 (engine env/default)", o.keepAlivePeriod)
	}
	if o.idleTimeout != 0 {
		t.Errorf("idleTimeout = %v, want 0 (engine env/default)", o.idleTimeout)
	}
	if o.handshakeTimeout != 0 {
		t.Errorf("handshakeTimeout = %v, want 0 (engine env/default)", o.handshakeTimeout)
	}
	if o.maxStreams != 0 {
		t.Errorf("maxStreams = %d, want 0 (engine env/default)", o.maxStreams)
	}
	if o.forwardMaxStreams != 0 {
		t.Errorf("forwardMaxStreams = %d, want 0 (engine env/default)", o.forwardMaxStreams)
	}
	if o.streamIdleTimeout != 0 {
		t.Errorf("streamIdleTimeout = %v, want 0 (disabled)", o.streamIdleTimeout)
	}
}

func TestToPeerConfig_ZeroDefaults(t *testing.T) {
	// A zero PeerConfig lets the engine apply QVOLE_* env vars and built-in
	// defaults; non-zero defaults here would shadow them for library users.
	cfg := toPeerConfig(defaultOptions())
	if cfg != (engine.PeerConfig{}) {
		t.Errorf("toPeerConfig(defaultOptions()) = %+v, want zero PeerConfig", cfg)
	}
}

func TestWithCode(t *testing.T) {
	o := defaultOptions()
	WithCode("my-secret")(o)
	if o.code != "my-secret" {
		t.Errorf("code = %q, want %q", o.code, "my-secret")
	}
}

func TestWithRelay(t *testing.T) {
	o := defaultOptions()
	WithRelay("relay.example.com:9009")(o)
	if o.relay != "relay.example.com:9009" {
		t.Errorf("relay = %q, want %q", o.relay, "relay.example.com:9009")
	}
}

func TestWithPunchTimeout(t *testing.T) {
	o := defaultOptions()
	WithPunchTimeout(5 * time.Second)(o)
	if o.punchTimeout != 5*time.Second {
		t.Errorf("punchTimeout = %v, want 5s", o.punchTimeout)
	}
}

func TestOptionsStacking(t *testing.T) {
	o := resolveOptions([]Option{
		WithCode("test-code"),
		WithRelay("custom:9009"),
		WithPunchTimeout(3 * time.Second),
	})
	if o.code != "test-code" {
		t.Errorf("code = %q", o.code)
	}
	if o.relay != "custom:9009" {
		t.Errorf("relay = %q", o.relay)
	}
	if o.punchTimeout != 3*time.Second {
		t.Errorf("punchTimeout = %v", o.punchTimeout)
	}
}

func TestResolveOptions_EmptyCode(t *testing.T) {
	o := resolveOptions(nil)
	if o.code != "" {
		t.Errorf("code should be empty, got %q", o.code)
	}
}

func TestValidate_CodeLength(t *testing.T) {
	// Empty code should be rejected.
	o := resolveOptions([]Option{WithRelay("r:1")})
	if err := validate(o); err == nil {
		t.Fatal("expected error for empty code")
	}

	// Too short.
	o = resolveOptions([]Option{WithCode("short"), WithRelay("r:1")})
	if err := validate(o); err == nil {
		t.Fatal("expected error for short code (7 chars)")
	}

	// Minimum length (8).
	o = resolveOptions([]Option{WithCode("12345678"), WithRelay("r:1")})
	if err := validate(o); err != nil {
		t.Fatalf("unexpected error for 8-char code: %v", err)
	}

	// Maximum length (256).
	long := make([]byte, 256)
	for i := range long {
		long[i] = 'a'
	}
	o = resolveOptions([]Option{WithCode(string(long)), WithRelay("r:1")})
	if err := validate(o); err != nil {
		t.Fatalf("unexpected error for 256-char code: %v", err)
	}

	// Too long.
	long = make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	o = resolveOptions([]Option{WithCode(string(long)), WithRelay("r:1")})
	if err := validate(o); err == nil {
		t.Fatal("expected error for long code (257 chars)")
	}
}

func TestValidate_MissingRelay(t *testing.T) {
	o := resolveOptions([]Option{WithCode("12345678")})
	if err := validate(o); err == nil {
		t.Fatal("expected error for missing relay")
	}
}

func TestWithStreamIdleTimeout(t *testing.T) {
	o := defaultOptions()
	WithStreamIdleTimeout(5 * time.Minute)(o)
	if o.streamIdleTimeout != 5*time.Minute {
		t.Errorf("streamIdleTimeout = %v, want 5min", o.streamIdleTimeout)
	}
	// A zero value disables the per-stream cap (the default).
	WithStreamIdleTimeout(0)(o)
	if o.streamIdleTimeout != 0 {
		t.Errorf("streamIdleTimeout = %v, want 0 after reset", o.streamIdleTimeout)
	}
}

func TestWithCommand(t *testing.T) {
	o := defaultOptions()
	WithCommand("uptime")(o)
	if o.command != "uptime" {
		t.Errorf("command = %q", o.command)
	}
}

func TestWithCmdMode(t *testing.T) {
	o := defaultOptions()
	WithCmdMode(true)(o)
	if !o.cmdMode {
		t.Error("cmdMode should be true")
	}
}

func TestWithLocalTunnel(t *testing.T) {
	o := defaultOptions()
	WithLocalTunnel("8080:localhost:80")(o)
	WithLocalTunnel("9090:localhost:9090")(o)
	if len(o.localTunnels) != 2 {
		t.Errorf("localTunnels = %d, want 2", len(o.localTunnels))
	}
	if o.localTunnels[0] != "8080:localhost:80" {
		t.Errorf("localTunnels[0] = %q", o.localTunnels[0])
	}
	if o.localTunnels[1] != "9090:localhost:9090" {
		t.Errorf("localTunnels[1] = %q", o.localTunnels[1])
	}
}

func TestWithRemoteTunnel(t *testing.T) {
	o := defaultOptions()
	WithRemoteTunnel("2222:localhost:22")(o)
	if len(o.remoteTunnels) != 1 {
		t.Errorf("remoteTunnels = %d, want 1", len(o.remoteTunnels))
	}
	if o.remoteTunnels[0] != "2222:localhost:22" {
		t.Errorf("remoteTunnels[0] = %q", o.remoteTunnels[0])
	}
}

func TestWithAllowAll(t *testing.T) {
	o := defaultOptions()
	if o.allowAll {
		t.Error("allowAll should be false by default")
	}
	WithAllowAll(true)(o)
	if !o.allowAll {
		t.Error("allowAll should be true")
	}
}

func TestWithAllowListen(t *testing.T) {
	o := defaultOptions()
	if len(o.allowListen) != 0 {
		t.Error("allowListen should be empty by default")
	}
	WithAllowListen("127.0.0.1:2222")(o)
	WithAllowListen(":8080")(o)
	if len(o.allowListen) != 2 {
		t.Errorf("allowListen = %d entries, want 2", len(o.allowListen))
	}
	if o.allowListen[0] != "127.0.0.1:2222" {
		t.Errorf("allowListen[0] = %q", o.allowListen[0])
	}
}

func TestWithAllowForward(t *testing.T) {
	o := defaultOptions()
	if len(o.allowForward) != 0 {
		t.Error("allowForward should be empty by default")
	}
	WithAllowForward("localhost:80")(o)
	if len(o.allowForward) != 1 {
		t.Errorf("allowForward = %d entries, want 1", len(o.allowForward))
	}
	if o.allowForward[0] != "localhost:80" {
		t.Errorf("allowForward[0] = %q", o.allowForward[0])
	}
}

func TestToPeerConfig(t *testing.T) {
	o := defaultOptions()
	o.punchTimeout = 5 * time.Second
	o.exchangeDeadline = 3 * time.Minute
	o.keepAlivePeriod = 5 * time.Second
	o.idleTimeout = 60 * time.Second
	o.handshakeTimeout = 15 * time.Second
	o.maxStreams = 50
	o.forwardMaxStreams = 150

	cfg := toPeerConfig(o)
	if cfg.PunchTimeout != 5*time.Second {
		t.Errorf("PunchTimeout = %v", cfg.PunchTimeout)
	}
	if cfg.ExchangeDeadline != 3*time.Minute {
		t.Errorf("ExchangeDeadline = %v", cfg.ExchangeDeadline)
	}
	if cfg.KeepAlivePeriod != 5*time.Second {
		t.Errorf("KeepAlivePeriod = %v", cfg.KeepAlivePeriod)
	}
	if cfg.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v", cfg.IdleTimeout)
	}
	if cfg.HandshakeTimeout != 15*time.Second {
		t.Errorf("HandshakeTimeout = %v", cfg.HandshakeTimeout)
	}
	if cfg.MaxStreams != 50 {
		t.Errorf("MaxStreams = %d", cfg.MaxStreams)
	}
	if cfg.ForwardMaxStreams != 150 {
		t.Errorf("ForwardMaxStreams = %d, want 150", cfg.ForwardMaxStreams)
	}
}

func TestProtocolVersion(t *testing.T) {
	if ProtocolVersion == "" {
		t.Error("ProtocolVersion should not be empty")
	}
}
