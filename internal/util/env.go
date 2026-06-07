package util

import (
	"os"
	"strconv"
	"time"
)

// EnvDuration reads a duration in milliseconds from the environment variable
// name and returns it as a time.Duration. Returns def if the variable is
// absent, unparseable, or <= 0.
func EnvDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return def
}

// EnvInt reads an integer from the environment variable name. Returns def if
// the variable is absent, unparseable, or <= 0.
func EnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
