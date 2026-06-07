package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var qvoleBin string

func TestMain(m *testing.M) {
	bin, err := buildQvole()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build qvole: %v\n", err)
		os.Exit(1)
	}
	qvoleBin = bin
	os.Setenv("QVOLE_PUNCH_TIMEOUT_MS", "3000")
	code := m.Run()
	os.RemoveAll(filepath.Dir(bin))
	os.Exit(code)
}

func buildQvole() (string, error) {
	tmpDir, err := os.MkdirTemp("", "qvole-integration-*")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}

	binPath := filepath.Join(tmpDir, "qvole")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/qvole")
	cmd.Dir = findModuleRoot()
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build: %w", err)
	}

	return binPath, nil
}

func findModuleRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for _, d := range []string{wd, filepath.Dir(wd)} {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	return filepath.Dir(wd)
}
