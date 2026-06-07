package util

import (
	"fmt"
	"log"
	"os"
	"time"
)

const prefixWidth = 10

var disableColors bool

// Debug controls whether Printf-level log messages are shown.
// Error, warning, success, and fatal messages are always shown regardless.
var Debug bool

func init() {
	if os.Getenv("NO_COLOR") != "" {
		disableColors = true
	}
}

const (
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

// Bold wraps s in ANSI bold escape codes unless NO_COLOR is set.
func Bold(s string) string {
	if disableColors {
		return s
	}
	return ansiBold + s + ansiReset
}

// Logger is a tagged, color-coded logger that writes to [log] output.
type Logger struct {
	Prefix string
	Color  string
}

func (l *Logger) logf(msgColor, format string, args ...any) string {
	ts := time.Now().Format("2006-01-02 15:04:05")
	tag := fmt.Sprintf("%-*s", prefixWidth, l.Prefix)
	msg := fmt.Sprintf(format, args...)
	if disableColors {
		return ts + "  " + tag + "  " + msg
	}
	if msgColor != "" {
		msg = msgColor + msg + ansiReset
	}
	return ansiDim + ts + ansiReset + "  " + l.Color + tag + ansiReset + "  " + msg
}

func (l *Logger) Printf(format string, args ...any) {
	if !Debug {
		return
	}
	log.Output(3, l.logf("", format, args...))
}

func (l *Logger) PrintfInfo(format string, args ...any) {
	log.Output(3, l.logf("", format, args...))
}

func (l *Logger) PrintfError(format string, args ...any) {
	log.Output(3, l.logf(ansiRed, format, args...))
}

func (l *Logger) PrintfWarn(format string, args ...any) {
	log.Output(3, l.logf(ansiYellow, format, args...))
}

func (l *Logger) PrintfSuccess(format string, args ...any) {
	log.Output(3, l.logf(ansiGreen, format, args...))
}

func (l *Logger) Fatalf(format string, args ...any) {
	log.Output(3, l.logf(ansiRed, format, args...))
	os.Exit(1)
}

var (
	LogPipe   = &Logger{"Pipe", "\033[36m"}      // cyan
	LogTunnel = &Logger{"Tunnel", "\033[32m"}    // green
	LogRelay  = &Logger{"Relay", "\033[33m"}     // yellow
	LogHole   = &Logger{"HolePunch", "\033[34m"} // blue
	LogSPAKE2 = &Logger{"SPAKE2", "\033[35m"}    // magenta
	LogCopy   = &Logger{"Copy", "\033[90m"}      // gray
	LogExec   = &Logger{"Exec", "\033[96m"}      // bright cyan
)
