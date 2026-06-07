package qvole

import (
	"testing"
	"time"
)

func TestDefaultOptions(t *testing.T) {
	o := defaultOptions()
	if o.relay != "" {
		t.Errorf("relay should be empty by default, got %q", o.relay)
	}
	if o.punchTimeout != 10*time.Second {
		t.Errorf("punchTimeout = %v, want 10s", o.punchTimeout)
	}
	if o.exchangeDeadline != 5*time.Minute {
		t.Errorf("exchangeDeadline = %v, want 5min", o.exchangeDeadline)
	}
	if o.keepAlivePeriod != 2*time.Second {
		t.Errorf("keepAlivePeriod = %v, want 2s", o.keepAlivePeriod)
	}
	if o.idleTimeout != 2*time.Minute {
		t.Errorf("idleTimeout = %v, want 2min", o.idleTimeout)
	}
	if o.handshakeTimeout != 30*time.Second {
		t.Errorf("handshakeTimeout = %v, want 30s", o.handshakeTimeout)
	}
	if o.maxStreams != 100 {
		t.Errorf("maxStreams = %d, want 100", o.maxStreams)
	}
	if o.forwardMaxStreams != 200 {
		t.Errorf("forwardMaxStreams = %d, want 200", o.forwardMaxStreams)
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

func TestWithAllowTunnel(t *testing.T) {
	o := defaultOptions()
	if o.allowTunnel {
		t.Error("allowTunnel should be false by default")
	}
	WithAllowTunnel(true)(o)
	if !o.allowTunnel {
		t.Error("allowTunnel should be true")
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
}

func TestProtocolVersion(t *testing.T) {
	if ProtocolVersion == "" {
		t.Error("ProtocolVersion should not be empty")
	}
}
