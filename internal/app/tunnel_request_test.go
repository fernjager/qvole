package app

import (
	"bufio"
	"strings"
	"testing"

	"github.com/fernjager/qvole/internal/engine"
)

func TestParseTunnelRequest_Local_3Part(t *testing.T) {
	spec, err := ParseTunnelRequest("8080:localhost:80", "L")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Type != "L" {
		t.Fatalf("expected type L, got %s", spec.Type)
	}
	if spec.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("expected listen 127.0.0.1:8080, got %s", spec.ListenAddr)
	}
	if spec.TargetAddr != "localhost:80" {
		t.Fatalf("expected target localhost:80, got %s", spec.TargetAddr)
	}
}

func TestParseTunnelRequest_Local_4Part(t *testing.T) {
	spec, err := ParseTunnelRequest("0.0.0.0:8080:localhost:80", "L")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.ListenAddr != "0.0.0.0:8080" {
		t.Fatalf("expected listen 0.0.0.0:8080, got %s", spec.ListenAddr)
	}
	if spec.TargetAddr != "localhost:80" {
		t.Fatalf("expected target localhost:80, got %s", spec.TargetAddr)
	}
}

func TestParseTunnelRequest_Remote_3Part(t *testing.T) {
	spec, err := ParseTunnelRequest("9000:localhost:22", "R")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Type != "R" {
		t.Fatalf("expected type R, got %s", spec.Type)
	}
	if spec.ListenAddr != "127.0.0.1:9000" {
		t.Fatalf("expected listen 127.0.0.1:9000, got %s", spec.ListenAddr)
	}
	if spec.TargetAddr != "localhost:22" {
		t.Fatalf("expected target localhost:22, got %s", spec.TargetAddr)
	}
}

func TestParseTunnelRequest_Remote_4Part(t *testing.T) {
	spec, err := ParseTunnelRequest("127.0.0.1:9000:localhost:22", "R")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.ListenAddr != "127.0.0.1:9000" {
		t.Fatalf("expected listen 127.0.0.1:9000, got %s", spec.ListenAddr)
	}
	if spec.TargetAddr != "localhost:22" {
		t.Fatalf("expected target localhost:22, got %s", spec.TargetAddr)
	}
}

func TestParseTunnelRequest_IPv6_Listen(t *testing.T) {
	spec, err := ParseTunnelRequest("[::1]:8080:localhost:80", "L")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.ListenAddr != "[::1]:8080" {
		t.Fatalf("expected listen [::1]:8080, got %s", spec.ListenAddr)
	}
	if spec.TargetAddr != "localhost:80" {
		t.Fatalf("expected target localhost:80, got %s", spec.TargetAddr)
	}
}

func TestParseTunnelRequest_IPv6_Target(t *testing.T) {
	spec, err := ParseTunnelRequest("8080:[::1]:80", "L")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("expected listen 127.0.0.1:8080, got %s", spec.ListenAddr)
	}
	if spec.TargetAddr != "[::1]:80" {
		t.Fatalf("expected target [::1]:80, got %s", spec.TargetAddr)
	}
}

func TestParseTunnelRequest_IPv6_Both(t *testing.T) {
	spec, err := ParseTunnelRequest("[::1]:8080:[::1]:80", "L")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.ListenAddr != "[::1]:8080" {
		t.Fatalf("expected listen [::1]:8080, got %s", spec.ListenAddr)
	}
	if spec.TargetAddr != "[::1]:80" {
		t.Fatalf("expected target [::1]:80, got %s", spec.TargetAddr)
	}
}

func TestSplitTunnelRequest_EdgeCases(t *testing.T) {
	cases := []struct {
		spec string
		want int
	}{
		{"8080:localhost:80", 3},
		{"127.0.0.1:8080:localhost:80", 4},
		{"[::1]:8080:localhost:80", 4},
		{"[::1]:8080:[::1]:80", 4},
		{"", 1},
		{"a:b", 2},
		{"a:b:c", 3},
		{"a:b:c:d", 4},
		{":::", 4},
		{"[::1", -1},
		{"]abc[", -1},
		{"[", -1},
		{"[[::]]:80:80", 3},
	}
	for _, tc := range cases {
		parts := SplitTunnelRequest(tc.spec)
		if tc.want == -1 {
			if parts != nil {
				t.Errorf("SplitTunnelRequest(%q) = %v, want nil", tc.spec, parts)
			}
		} else if parts == nil {
			t.Errorf("SplitTunnelRequest(%q) = nil, want %d parts", tc.spec, tc.want)
		} else if len(parts) != tc.want {
			t.Errorf("SplitTunnelRequest(%q) = %v (%d parts), want %d parts", tc.spec, parts, len(parts), tc.want)
		}
	}
}

func TestParseTunnelRequest_Invalid(t *testing.T) {
	cases := []struct {
		spec string
		typ  string
	}{
		{"invalid", "L"},
		{"a:b:c:d:e", "L"},
		{"", "L"},
		{"a:b", "L"},
		{"8080", "L"},
		{"[::1:8080:localhost:80", "L"},
	}
	for _, tc := range cases {
		_, err := ParseTunnelRequest(tc.spec, tc.typ)
		if err == nil {
			t.Errorf("expected error for spec %q type %q", tc.spec, tc.typ)
		}
	}
}

func TestTunnelConstants(t *testing.T) {
	if n := engine.GetForwardMaxStreams(); n != 200 {
		t.Errorf("engine.GetForwardMaxStreams() = %d, want 200", n)
	}
	if maxTunnelRequests != 100 {
		t.Errorf("maxTunnelRequests = %d, want 100", maxTunnelRequests)
	}
	if scannerMaxTokenSize != 4096 {
		t.Errorf("scannerMaxTokenSize = %d, want 4096", scannerMaxTokenSize)
	}
}

func TestTunnelRequest_Struct(t *testing.T) {
	s := TunnelRequest{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"}
	if s.Type != "L" {
		t.Errorf("typ = %s, want L", s.Type)
	}
	if s.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("listenAddr = %s, want 127.0.0.1:8080", s.ListenAddr)
	}
	if s.TargetAddr != "8.8.8.8:53" {
		t.Errorf("targetAddr = %s, want 8.8.8.8:53", s.TargetAddr)
	}
}

func TestParseTunnelRequest_PortOnly(t *testing.T) {
	_, err := ParseTunnelRequest("8080", "L")
	if err == nil {
		t.Fatal("expected error for single-part spec")
	}
}

func TestReadRequestsFromStream_RejectsInvalidType(t *testing.T) {
	validCases := []string{
		"L 127.0.0.1:8080 localhost:80\nEND\n",
		"R 127.0.0.1:8080 localhost:80\nEND\n",
		"L 127.0.0.1:8080 localhost:80\nR 127.0.0.1:9090 localhost:22\nEND\n",
	}
	for i, input := range validCases {
		scanner := bufio.NewScanner(strings.NewReader(input))
		scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
		reqs, err := readRequestsFromStream(scanner)
		if err != nil {
			t.Errorf("valid case %d: unexpected error: %v", i, err)
		}
		if len(reqs) == 0 {
			t.Errorf("valid case %d: expected non-empty requests", i)
		}
	}

	invalidCases := []string{
		"X 127.0.0.1:8080 localhost:80\nEND\n",
		"LR 127.0.0.1:8080 localhost:80\nEND\n",
		"l 127.0.0.1:8080 localhost:80\nEND\n",
		" 127.0.0.1:8080 localhost:80\nEND\n",
	}
	for i, input := range invalidCases {
		scanner := bufio.NewScanner(strings.NewReader(input))
		scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
		_, err := readRequestsFromStream(scanner)
		if err == nil {
			t.Errorf("invalid case %d: expected error for input %q", i, input)
		}
	}
}
