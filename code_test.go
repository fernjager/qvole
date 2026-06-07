package qvole

import (
	"strings"
	"testing"
)

func TestGenerateCode_Format(t *testing.T) {
	code, err := GenerateCode()
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if code == "" {
		t.Fatal("code is empty")
	}
	parts := strings.Split(code, "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts, got %d: %q", len(parts), code)
	}
	if len(parts[0]) != 4 {
		t.Fatalf("first part should be 4 digits, got %q", parts[0])
	}
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			t.Fatalf("first part should be all digits, got %q", parts[0])
		}
	}
}

func TestNameplate_Generated(t *testing.T) {
	code, _ := GenerateCode()
	np := Nameplate(code)
	if len(np) != 4 {
		t.Fatalf("nameplate for generated code should be 4 chars, got %q (len=%d)", np, len(np))
	}
	if code[:4] != np {
		t.Fatalf("nameplate %q should match code prefix %q", np, code[:4])
	}
}

func TestNameplate_Arbitrary(t *testing.T) {
	np := Nameplate("my-secret-code")
	if len(np) != 8 {
		t.Fatalf("nameplate for arbitrary code should be 8 hex chars, got %q (len=%d)", np, len(np))
	}
	for _, c := range np {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex {
			t.Fatalf("nameplate should be hex, got %q", np)
		}
	}
}

func TestNameplate_Deterministic(t *testing.T) {
	a := Nameplate("my-secret-code")
	b := Nameplate("my-secret-code")
	if a != b {
		t.Fatalf("nameplate should be deterministic: %q vs %q", a, b)
	}
	if len(a) != 8 {
		t.Fatalf("nameplate should be 8 hex chars, got %q", a)
	}
}

func TestNameplate_DifferentCodes(t *testing.T) {
	a := Nameplate("code-a")
	b := Nameplate("code-b")
	if a == b {
		t.Fatal("different codes should produce different nameplates")
	}
}

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

func TestParseTunnelRequest_Remote_4Part(t *testing.T) {
	spec, err := ParseTunnelRequest("0.0.0.0:9000:localhost:22", "R")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Type != "R" {
		t.Fatalf("expected type R, got %s", spec.Type)
	}
	if spec.ListenAddr != "0.0.0.0:9000" {
		t.Fatalf("expected listen 0.0.0.0:9000, got %s", spec.ListenAddr)
	}
	if spec.TargetAddr != "localhost:22" {
		t.Fatalf("expected target localhost:22, got %s", spec.TargetAddr)
	}
}

func TestParseTunnelRequest_Invalid(t *testing.T) {
	_, err := ParseTunnelRequest("invalid", "L")
	if err == nil {
		t.Fatal("expected error for invalid spec")
	}
}

func TestBufferPool(t *testing.T) {
	buf := BufferPool.Get().([]byte)
	if len(buf) != 32768 {
		t.Errorf("BufferPool should return 32 KB buffers, got %d", len(buf))
	}
	PutBuffer(buf)
}
