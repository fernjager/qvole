package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/fernjager/qvole/internal/app"
	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
	"github.com/fernjager/qvole/relay"
)

const (
	minCodeLen        = 8
	maxCodeLen        = 256
	defaultRelayAddr  = "relay.qvole.dev:9009"
	defaultListenAddr = ":9009"
)

var version = "0.1.0"
var debug bool

func main() {
	log.SetFlags(0)
	_ = os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "1")

	for _, a := range os.Args[1:] {
		if a == "-v" || a == "--version" {
			fmt.Fprintf(os.Stderr, "qvole %s (protocol v%s)\n", version, engine.ProtocolVersion)
			return
		}
	}

	if len(os.Args) < 2 {
		printUsage()
		return
	}

	switch os.Args[1] {
	case "relay":
		runRelay(os.Args[2:])
	case "exec":
		if err := runExec(os.Args[2:]); err != nil {
			var ec exitCodeError
			if errors.As(err, &ec) {
				os.Exit(ec.code)
			}
			fatalf(util.LogExec, "%v", err)
		}
	case "tunnel":
		runTunnel(os.Args[2:])
	case "pipe":
		runPipe(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
	default:
		printUsage()
	}
}

func resolveRelay(relayAddr string) string {
	if relayAddr != "" {
		return relayAddr
	}
	return defaultRelayAddr
}

func resolveCode(flagVal string) (string, error) {
	if flagVal == "" {
		flagVal = os.Getenv("QVOLE_CODE")
	}
	if flagVal == "" {
		return "", nil
	}
	if len(flagVal) < minCodeLen {
		return "", fmt.Errorf("code is too short (%d chars); minimum is %d", len(flagVal), minCodeLen)
	}
	if len(flagVal) > maxCodeLen {
		return "", fmt.Errorf("code exceeds maximum length of %d characters", maxCodeLen)
	}
	return flagVal, nil
}

type commonFlags struct {
	relay string
}

func setupCommonFlags(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.relay, "relay", "", "Relay address (host:port)")
	fs.BoolVar(&debug, "debug", false, "Verbose debug logging to stderr")
	return c
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), interruptSignals()...)
}

func maybeFatal(l *util.Logger, err error) {
	if err != nil && err != context.Canceled {
		fatalf(l, "%v", err)
	}
}

func fatalf(l *util.Logger, format string, args ...any) {
	l.Fatalf(format, args...)
}

func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ContinueOnError)
	listen := fs.String("listen", defaultListenAddr, "UDP listen address (host:port)")
	fs.BoolVar(&debug, "debug", false, "Verbose debug logging to stderr")
	if err := fs.Parse(args); err != nil {
		fatalf(util.LogRelay, "flag: %v", err)
	}
	util.Debug = debug

	ctx, cancel := signalContext()
	defer cancel()

	if err := relay.RunRelay(ctx, *listen); err != nil {
		fatalf(util.LogRelay, "%v", err)
	}
}

func runPipe(args []string) {
	fs := flag.NewFlagSet("qvole", flag.ContinueOnError)
	cf := setupCommonFlags(fs)
	code := fs.String("code", "", "Connection code (or $QVOLE_CODE)")
	stats := fs.Bool("stats", false, "Log transfer statistics to stderr")
	if err := fs.Parse(args); err != nil {
		fatalf(util.LogPipe, "flag: %v", err)
	}
	util.Debug = debug

	relayAddr := resolveRelay(cf.relay)

	finalCode, err := resolveCode(*code)
	if err != nil {
		fatalf(util.LogPipe, "%v", err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	maybeFatal(util.LogPipe, engine.RunPipe(ctx, relayAddr, finalCode, *stats))
}

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// exitCodeError wraps an exit code so it can be propagated through the call
// stack and converted to os.Exit only at the top level, after all deferred
// cleanup (QUIC close frames, TLS session tickets, etc.) has run.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

func runExec(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	cf := setupCommonFlags(fs)
	code := fs.String("code", "", "Connection code (or $QVOLE_CODE)")
	cmdFlag := fs.String("cmd", "", "Run command")
	if err := fs.Parse(args); err != nil {
		fatalf(util.LogExec, "flag: %v", err)
	}
	util.Debug = debug

	relayAddr := resolveRelay(cf.relay)

	finalCode, err := resolveCode(*code)
	if err != nil {
		fatalf(util.LogExec, "%v", err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	err = app.RunExec(ctx, relayAddr, finalCode, *cmdFlag, *cmdFlag != "")
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitCodeError{code: exitErr.ExitCode()}
	}
	if err != nil && err != context.Canceled {
		fatalf(util.LogExec, "%v", err)
	}
	return nil
}

func runTunnel(args []string) {
	fs := flag.NewFlagSet("tunnel", flag.ContinueOnError)
	cf := setupCommonFlags(fs)
	code := fs.String("code", "", "Connection code (or $QVOLE_CODE)")
	allowTunnel := fs.Bool("allow-tunnel", false, "Allow incoming tunnel streams from peer")
	var localTunnels stringSlice
	var remoteTunnels stringSlice
	fs.Var(&localTunnels, "L", "Local tunnel request ([addr:]port:host:port)")
	fs.Var(&remoteTunnels, "R", "Remote tunnel request ([addr:]port:host:port)")
	if err := fs.Parse(args); err != nil {
		fatalf(util.LogTunnel, "flag: %v", err)
	}
	util.Debug = debug

	relayAddr := resolveRelay(cf.relay)

	finalCode, err := resolveCode(*code)
	if err != nil {
		fatalf(util.LogTunnel, "%v", err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	maybeFatal(util.LogTunnel, app.RunTunnel(ctx, relayAddr, finalCode, localTunnels, remoteTunnels, *allowTunnel))
}

func printUsage() {
	fmt.Fprint(os.Stderr, `qvole `+version+`: Fast tunnels over QUIC, burrowed through NATs.

Commands:

  pipe [--stats]              Bidirectional stream between two peers over QUIC

  tunnel                      TCP port tunneling over P2P QUIC
    -L [addr:]port:host:port    ...tunnel local port to peer's host:port
    -R [addr:]port:host:port    ...peer tunnels to your host:port
    --allow-tunnel              ...accept tunnel requests (peer can connect to your local services)

  exec                        Execute a command or connect to an exec peer
    --cmd                       ...run command, pipe stdin/stdout to peer

  relay                       Relay server (UDP)
    --listen <addr>             ...listen address (default :9009)

Common Flags (pipe / exec / tunnel):
  --code CODE                 connection code (or $QVOLE_CODE environment var)
  [--relay addr]              relay server address (host:port)
  [--debug]                   verbose debug logging to stderr
  [--stats]                   log TX/RX throughput to stderr every 2 s (pipe-only)

Examples:

  # Send a file
  alice$ qvole pipe > out                                             # prints connection code, waits for bob
  bob$   QVOLE_CODE=CODE qvole pipe < in                              # bob connects, sending file to alice

  # Send a directory
  alice$ tar czf - dir/ | qvole pipe                                  # alice streams tar of dir/ to bob
  bob$   QVOLE_CODE=CODE qvole pipe | tar xzf -                       # bob receives and extracts

  # Port Forwarding
  alice$ qvole tunnel -L 8080:localhost:80 -R 2222:localhost:22       # alice configures port forwarding requests
  bob$   QVOLE_CODE=CODE qvole tunnel --allow-tunnel                  # bob needs to explicitly allow tunnels
  alice$ curl localhost:8080                                          # alice can now reach bob's :80
  bob$   ssh -p 2222 localhost                                        # bob can now reach alice's sshd

  # Remote command (alice hosts the command, bob connects)
  alice$ qvole exec --cmd "uptime"                                    # command runs on alice, bob sees output
  alice$ qvole exec --cmd "script -q bash /dev/null"                 # alice gives bob remote shell
  bob$   QVOLE_CODE=CODE qvole exec

`)
}
