package app

import (
	"strings"
	"testing"
)

func TestTunnelAllow_Accepts(t *testing.T) {
	var empty TunnelAllow
	if empty.Accepts() {
		t.Error("empty allowlist should not accept anything")
	}
	listenOnly := TunnelAllow{Listen: []string{"127.0.0.1:2222"}}
	if !listenOnly.Accepts() {
		t.Error("listen entry should make allowlist accept")
	}
	forwardOnly := TunnelAllow{Forward: []string{"localhost:80"}}
	if !forwardOnly.Accepts() {
		t.Error("forward entry should make allowlist accept")
	}
	if !((TunnelAllow{All: true}).Accepts()) {
		t.Error("All should make allowlist accept")
	}
}

func TestTunnelAllow_Validate(t *testing.T) {
	valid := []TunnelAllow{
		{},
		{Listen: []string{"127.0.0.1:2222"}},
		{Listen: []string{":8081"}},
		{Forward: []string{"localhost:80"}},
		{Forward: []string{"[::1]:80"}},
	}
	for _, a := range valid {
		if err := a.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", a, err)
		}
	}

	invalid := []TunnelAllow{
		{Listen: []string{"8081"}},
		{Listen: []string{"localhost"}},
		{Listen: []string{"localhost:"}},
		{Forward: []string{"host:port:extra"}},
		{Forward: []string{"127.0.0.1:80", "nonsense"}},
	}
	for _, a := range invalid {
		if err := a.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want error", a)
		}
	}

	err := (TunnelAllow{Listen: []string{"8081"}, Forward: []string{"bad"}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "-aL") || !strings.Contains(err.Error(), "-aF") {
		t.Errorf("Validate error should name both flags, got %v", err)
	}
}

func TestTunnelAllow_AllowsListen(t *testing.T) {
	a := TunnelAllow{Listen: []string{"127.0.0.1:2222", ":8081"}}

	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:2222", true},
		{"127.0.0.1:8081", true}, // wildcard host entry
		{"0.0.0.0:8081", true},   // wildcard host entry
		{"127.0.0.2:2222", false},
		{"0.0.0.0:2222", false},
		{"127.0.0.1:2223", false},
		{"127.0.0.1", false}, // no port
		{"", false},
	}
	for _, tc := range cases {
		if got := a.AllowsListen(tc.addr); got != tc.want {
			t.Errorf("AllowsListen(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}

	if (TunnelAllow{}).AllowsListen("127.0.0.1:2222") {
		t.Error("empty allowlist must not allow any listen address")
	}
	if !((TunnelAllow{All: true}).AllowsListen("0.0.0.0:1")) {
		t.Error("All must allow any listen address")
	}
}

func TestTunnelAllow_AllowsForward(t *testing.T) {
	a := TunnelAllow{Forward: []string{"localhost:80", "[::1]:443"}}

	cases := []struct {
		addr string
		want bool
	}{
		{"localhost:80", true},
		{"LOCALHOST:80", true}, // case-insensitive host
		{"127.0.0.1:80", false},
		{"localhost:8080", false},
		{"[::1]:443", true},
		{"::1:443", false}, // unbracketed IPv6 is not a valid host:port
		{"[::1]:444", false},
	}
	for _, tc := range cases {
		if got := a.AllowsForward(tc.addr); got != tc.want {
			t.Errorf("AllowsForward(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}

	if (TunnelAllow{}).AllowsForward("localhost:80") {
		t.Error("empty allowlist must not allow any forward target")
	}
	if !((TunnelAllow{All: true}).AllowsForward("10.0.0.5:22")) {
		t.Error("All must allow any forward target")
	}
}
