package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fernjager/qvole/internal/util"
)

func TestResolveCode_Empty(t *testing.T) {
	code, err := resolveCode("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "" {
		t.Fatalf("expected empty code, got %q", code)
	}
}

func TestResolveCode_TooShort(t *testing.T) {
	const minLen = 8
	short := strings.Repeat("x", minLen-1)
	_, err := resolveCode(short)
	if err == nil {
		t.Fatal("expected error for short code")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("error should mention 'too short', got: %v", err)
	}
	if !strings.Contains(err.Error(), "8") {
		t.Errorf("error should mention minimum 8")
	}
}

func TestResolveCode_TooLong(t *testing.T) {
	const maxLen = 256
	long := strings.Repeat("x", maxLen+1)
	_, err := resolveCode(long)
	if err == nil {
		t.Fatal("expected error for long code")
	}
}

func TestResolveCode_BoundaryMin(t *testing.T) {
	code := strings.Repeat("x", 8)
	c, err := resolveCode(code)
	if err != nil {
		t.Fatalf("expected valid for exactly 8 chars: %v", err)
	}
	if c != code {
		t.Fatalf("got %q, want %q", c, code)
	}
}

func TestResolveCode_BoundaryMax(t *testing.T) {
	code := strings.Repeat("x", 256)
	c, err := resolveCode(code)
	if err != nil {
		t.Fatalf("expected valid for exactly 256 chars: %v", err)
	}
	if c != code {
		t.Fatalf("got %q, want %q", c, code)
	}
}

func TestResolveCode_EnvFallback(t *testing.T) {
	os.Setenv("QVOLE_CODE", "env-secret-1234")
	defer os.Unsetenv("QVOLE_CODE")
	code, err := resolveCode("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "env-secret-1234" {
		t.Fatalf("expected env value, got %q", code)
	}
}

func TestResolveCode_FlagPrecedesEnv(t *testing.T) {
	os.Setenv("QVOLE_CODE", "env-secret")
	defer os.Unsetenv("QVOLE_CODE")
	code, err := resolveCode("flag-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != "flag-secret" {
		t.Fatalf("expected flag value, got %q", code)
	}
}

func TestStringSlice_Empty(t *testing.T) {
	var s stringSlice
	if s.String() != "" {
		t.Fatalf("expected empty, got %q", s.String())
	}
}

func TestStringSlice_Set(t *testing.T) {
	var s stringSlice
	if err := s.Set("a"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if err := s.Set("b"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if s.String() != "a, b" {
		t.Fatalf("expected \"a, b\", got %q", s.String())
	}
}

func TestStringSlice_FlagValue(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var fw stringSlice
	fs.Var(&fw, "L", "test")
	if err := fs.Parse([]string{"-L", "8080:localhost:80", "-L", "9090:localhost:90"}); err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(fw) != 2 {
		t.Fatalf("expected 2 values, got %d", len(fw))
	}
	if fw[0] != "8080:localhost:80" {
		t.Fatalf("expected first value, got %q", fw[0])
	}
	if fw[1] != "9090:localhost:90" {
		t.Fatalf("expected second value, got %q", fw[1])
	}
}

func TestMaybeFatal_Nil(t *testing.T) {
	maybeFatal(util.LogPipe, nil)
}

func TestMaybeFatal_Canceled(t *testing.T) {
	maybeFatal(util.LogPipe, context.Canceled)
}

func TestMaybeFatal_WrappedCanceled(t *testing.T) {
	// A wrapped cancellation (e.g. "config exchange: %w") must also shut
	// down quietly instead of exiting via fatalf.
	maybeFatal(util.LogPipe, fmt.Errorf("config exchange: %w", context.Canceled))
}

func TestValidateTunnelConfig_Valid(t *testing.T) {
	err := validateTunnelConfig(
		[]string{"8080:localhost:80", ":9090:127.0.0.1:9090", "[::1]:8081:localhost:81"},
		[]string{"2222:localhost:22"},
		[]string{"127.0.0.1:2222"}, []string{"localhost:80"}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTunnelConfig_InvalidPort(t *testing.T) {
	for _, spec := range []string{"abc:host:80", "8080:host:xyz", "8080:host:99999", "0:host:80", "8080:host:0"} {
		if err := validateTunnelConfig([]string{spec}, nil, nil, nil, false); err == nil {
			t.Errorf("expected error for -L %q", spec)
		}
		if err := validateTunnelConfig(nil, []string{spec}, nil, nil, false); err == nil {
			t.Errorf("expected error for -R %q", spec)
		}
	}
}

func TestValidateTunnelConfig_TooManySpecs(t *testing.T) {
	specs := make([]string, maxTunnelSpecs+1)
	for i := range specs {
		specs[i] = fmt.Sprintf("%d:localhost:80", 10000+i)
	}
	err := validateTunnelConfig(specs, nil, nil, nil, false)
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("expected 'too many' error, got %v", err)
	}
}

func TestResolveRelay_Custom(t *testing.T) {
	got := resolveRelay("1.2.3.4:9009")
	if got != "1.2.3.4:9009" {
		t.Fatalf("resolveRelay custom = %q, want %q", got, "1.2.3.4:9009")
	}
}

func TestResolveRelay_Default(t *testing.T) {
	got := resolveRelay("")
	if got != defaultRelayAddr {
		t.Fatalf("resolveRelay empty = %q, want %q", got, defaultRelayAddr)
	}
}
