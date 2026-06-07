package tests

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func testCodeValidationShort(t *testing.T, relayAddr string) {
	args := pipeArgs(relayAddr, "short")
	cmd := exec.Command(qvoleBin, args...)
	cmd.Stdin = strings.NewReader("")
	out, _ := cmd.CombinedOutput()
	if !bytes.Contains(bytes.ToLower(out), []byte("too short")) {
		t.Errorf("expected 'too short' for code 'short', got: %s", string(out))
	}
}

func testCodeValidationLong(t *testing.T, relayAddr string) {
	longCode := strings.Repeat("x", 257)
	args := pipeArgs(relayAddr, longCode)
	cmd := exec.Command(qvoleBin, args...)
	cmd.Stdin = strings.NewReader("")
	out, _ := cmd.CombinedOutput()
	if !bytes.Contains(bytes.ToLower(out), []byte("exceeds maximum")) {
		t.Errorf("expected 'exceeds maximum' for long code, got: %s", string(out))
	}
}

func TestCodeValidation(t *testing.T) {
	addr, cleanup := testRelay(t)
	defer cleanup()

	testCodeValidationShort(t, addr)
	testCodeValidationLong(t, addr)
}

func TestVersionFlag(t *testing.T) {
	for _, flag := range []string{"-v", "--version"} {
		cmd := exec.Command(qvoleBin, flag)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("%s: %v", flag, err)
			continue
		}
		if !bytes.Contains(out, []byte("qvole")) {
			t.Errorf("%s: output %q should contain 'qvole'", flag, string(out))
		}
		if !bytes.Contains(out, []byte("protocol")) {
			t.Errorf("%s: output %q should contain 'protocol'", flag, string(out))
		}
	}
}

func TestHelpFlag(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "help"} {
		cmd := exec.Command(qvoleBin, flag)
		out, _ := cmd.CombinedOutput()
		if !bytes.Contains(out, []byte("Commands:")) {
			t.Errorf("%s: output missing 'Commands:', got: %q", flag, string(out))
		}
	}
}

func TestNoArgs(t *testing.T) {
	cmd := exec.Command(qvoleBin)
	out, _ := cmd.CombinedOutput()
	if !bytes.Contains(out, []byte("Commands:")) {
		t.Errorf("no args: output missing 'Commands:', got: %q", string(out))
	}
}

func TestUnknownSubcommand(t *testing.T) {
	cmd := exec.Command(qvoleBin, "foobar")
	out, _ := cmd.CombinedOutput()
	if !bytes.Contains(out, []byte("Commands:")) {
		t.Errorf("unknown subcommand: output missing 'Commands:', got: %q", string(out))
	}
}
