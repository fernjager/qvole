package main

import (
	"strings"
	"testing"
)

func TestPeerHintBlock(t *testing.T) {
	got := peerHintBlock("QVOLE_CODE=abc qvole pipe")
	want := "# peer command\nQVOLE_CODE=abc qvole pipe\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTunnelPeerCommand(t *testing.T) {
	cases := []struct {
		name    string
		local   []string
		remote  []string
		want    string
		wantErr bool
	}{
		{
			name:   "mixed L and R",
			local:  []string{"8080:localhost:80"},
			remote: []string{"2222:localhost:22"},
			want:   "QVOLE_CODE=abc qvole tunnel -aL 127.0.0.1:2222 -aF localhost:80",
		},
		{
			name:  "multiple L dedupe targets",
			local: []string{"8080:localhost:80", "8081:localhost:80"},
			want:  "QVOLE_CODE=abc qvole tunnel -aF localhost:80",
		},
		{
			name:   "multiple R dedupe listens",
			remote: []string{"2222:localhost:22", "2223:localhost:22"},
			want:   "QVOLE_CODE=abc qvole tunnel -aL 127.0.0.1:2222 -aL 127.0.0.1:2223",
		},
		{
			name:   "explicit listen host",
			remote: []string{"0.0.0.0:2222:localhost:22"},
			want:   "QVOLE_CODE=abc qvole tunnel -aL 0.0.0.0:2222",
		},
		{
			name: "no requests",
			want: "",
		},
		{
			name:    "invalid spec",
			local:   []string{"not-a-spec"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tunnelPeerCommand("abc", tc.local, tc.remote)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateTunnelConfig(t *testing.T) {
	cases := []struct {
		name         string
		local        []string
		remote       []string
		allowListen  []string
		allowForward []string
		allowAll     bool
		wantErr      bool
	}{
		{name: "empty config"},
		{name: "valid L and R", local: []string{"8080:localhost:80"}, remote: []string{"2222:localhost:22"}},
		{name: "valid allowlist", allowListen: []string{"127.0.0.1:2222"}, allowForward: []string{"localhost:80"}},
		{name: "wildcard allowlist host", allowListen: []string{":8081"}},
		{name: "allow all", allowAll: true},
		{name: "bad L spec", local: []string{"not-a-spec"}, wantErr: true},
		{name: "bad R spec", remote: []string{"a:b:c:d:e"}, wantErr: true},
		{name: "bad allowListen", allowListen: []string{"8081"}, wantErr: true},
		{name: "bad allowForward", allowForward: []string{"localhost"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTunnelConfig(tc.local, tc.remote, tc.allowListen, tc.allowForward, tc.allowAll)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	// Error messages name the offending flag so typos fail fast and clearly.
	err := validateTunnelConfig(nil, nil, []string{"8081"}, nil, false)
	if err == nil || !strings.Contains(err.Error(), "-aL") {
		t.Errorf("expected error naming -aL, got %v", err)
	}
}

func TestResolveOrGenerateCode(t *testing.T) {
	code, generated, err := resolveOrGenerateCode("")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !generated {
		t.Error("expected generated=true for empty input")
	}
	if len(code) < 8 || !strings.Contains(code, "-") {
		t.Errorf("expected a generated code, got %q", code)
	}

	code2, generated2, err := resolveOrGenerateCode("my-code-1234")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if generated2 {
		t.Error("expected generated=false for provided code")
	}
	if code2 != "my-code-1234" {
		t.Errorf("expected passthrough code, got %q", code2)
	}

	if _, _, err := resolveOrGenerateCode("short"); err == nil {
		t.Error("expected error for code below minimum length")
	}
}
