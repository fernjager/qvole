package app

import (
	"context"
	"net"
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

func TestTunnelAllow_ForwardIPs(t *testing.T) {
	// IP-literal allowlist: exact match allowed; wrong host or port rejected.
	a := TunnelAllow{Forward: []string{"192.0.2.5:8080"}}
	ips, port, ok := a.ForwardIPs(context.Background(), "192.0.2.5:8080")
	if !ok || port != "8080" || len(ips) != 1 || !ips[0].Equal(net.ParseIP("192.0.2.5")) {
		t.Errorf("ForwardIPs exact match = ok:%v port:%q ips:%v, want 1 allowed ip 192.0.2.5:8080", ok, port, ips)
	}
	if _, _, ok := a.ForwardIPs(context.Background(), "192.0.2.6:8080"); ok {
		t.Error("ForwardIPs must reject a target host not in the allowlist")
	}
	if _, _, ok := a.ForwardIPs(context.Background(), "192.0.2.5:8081"); ok {
		t.Error("ForwardIPs must reject a target port not in the allowlist")
	}

	// Empty allowlist accepts nothing.
	if _, _, ok := (TunnelAllow{}).ForwardIPs(context.Background(), "192.0.2.5:8080"); ok {
		t.Error("empty allowlist must not allow any forward target")
	}
}

func TestTunnelAllow_ForwardIPs_WildcardAndAll(t *testing.T) {
	wc := TunnelAllow{Forward: []string{":8080"}}
	if _, _, ok := wc.ForwardIPs(context.Background(), "192.0.2.9:8080"); !ok {
		t.Error("wildcard :port should allow any host on that port")
	}
	if _, _, ok := wc.ForwardIPs(context.Background(), "192.0.2.9:8081"); ok {
		t.Error("wildcard :port must reject other ports")
	}
	if _, _, ok := (TunnelAllow{All: true}).ForwardIPs(context.Background(), "8.8.8.8:53"); !ok {
		t.Error("All must allow any forward target")
	}
}

func TestTunnelAllow_Resolve_Alias(t *testing.T) {
	// After Resolve(), a 127.0.0.1 rule matches a target spelled as localhost,
	// because ForwardIPs resolves both sides to an IP before matching.
	a := TunnelAllow{Forward: []string{"127.0.0.1:8080"}}
	a.Resolve()
	ips, port, ok := a.ForwardIPs(context.Background(), "localhost:8080")
	if !ok {
		t.Skip("localhost did not resolve to a loopback address in this environment")
	}
	if port != "8080" {
		t.Errorf("alias port = %q, want 8080", port)
	}
	found127 := false
	for _, ip := range ips {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			found127 = true
		}
	}
	if !found127 {
		t.Errorf("alias allowed IPs = %v, want to include 127.0.0.1", ips)
	}
}
