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

	"github.com/fernjager/qvole"
	"github.com/fernjager/qvole/internal/app"
	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
	"github.com/fernjager/qvole/relay"
)

const (
	defaultRelayAddr  = "relay.qvole.dev:9009"
	defaultListenAddr = ":9009"
)

var version = "0.2.1"
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
	if len(flagVal) < qvole.MinCodeLen {
		return "", fmt.Errorf("code is too short (%d chars); minimum is %d", len(flagVal), qvole.MinCodeLen)
	}
	if len(flagVal) > qvole.MaxCodeLen {
		return "", fmt.Errorf("code exceeds maximum length of %d characters", qvole.MaxCodeLen)
	}
	return flagVal, nil
}

// resolveOrGenerateCode returns the connection code, resolving it from the
// flag or $QVOLE_CODE, or generating a fresh one when absent. When generated,
// the caller prints it (and optionally a peer command hint) to stderr.
func resolveOrGenerateCode(flagVal string) (code string, generated bool, err error) {
	code, err = resolveCode(flagVal)
	if err != nil || code != "" {
		return code, false, err
	}
	code, err = util.GenerateCode()
	if err != nil {
		return "", false, fmt.Errorf("generate code: %w", err)
	}
	return code, true, nil
}

// validateTunnelConfig parses every -L/-R spec and allowlist entry up front so
// typos fail before a connection code is printed. It mirrors the validation
// app.RunTunnel performs, with the same error messages.
func validateTunnelConfig(localTunnels, remoteTunnels, allowListen, allowForward []string, allowAll bool) error {
	for _, f := range localTunnels {
		if _, err := app.ParseTunnelRequest(f, "L"); err != nil {
			return fmt.Errorf("invalid -L spec %q: %w", f, err)
		}
	}
	for _, f := range remoteTunnels {
		if _, err := app.ParseTunnelRequest(f, "R"); err != nil {
			return fmt.Errorf("invalid -R spec %q: %w", f, err)
		}
	}
	return (app.TunnelAllow{All: allowAll, Listen: allowListen, Forward: allowForward}).Validate()
}

// tunnelPeerCommand builds the peer's ready-to-paste tunnel command from our
// -L/-R requests: each -L needs the peer's -aF <target>, each -R the peer's
// -aL <listen>. Returns ("", nil) when there is nothing to suggest.
func tunnelPeerCommand(code string, localTunnels, remoteTunnels []string) (string, error) {
	seenListen := make(map[string]bool)
	seenForward := make(map[string]bool)
	var aL, aF []string
	for _, spec := range remoteTunnels {
		req, err := app.ParseTunnelRequest(spec, "R")
		if err != nil {
			return "", err
		}
		if !seenListen[req.ListenAddr] {
			seenListen[req.ListenAddr] = true
			aL = append(aL, req.ListenAddr)
		}
	}
	for _, spec := range localTunnels {
		req, err := app.ParseTunnelRequest(spec, "L")
		if err != nil {
			return "", err
		}
		if !seenForward[req.TargetAddr] {
			seenForward[req.TargetAddr] = true
			aF = append(aF, req.TargetAddr)
		}
	}
	if len(aL) == 0 && len(aF) == 0 {
		return "", nil
	}
	cmd := "QVOLE_CODE=" + code + " qvole tunnel"
	for _, v := range aL {
		cmd += " -aL " + v
	}
	for _, v := range aF {
		cmd += " -aF " + v
	}
	return cmd, nil
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

	finalCode, generated, err := resolveOrGenerateCode(*code)
	if err != nil {
		fatalf(util.LogPipe, "%v", err)
	}
	if generated {
		fmt.Fprintf(os.Stderr, "QVOLE_CODE=%s\n", finalCode)
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

	finalCode, generated, err := resolveOrGenerateCode(*code)
	if err != nil {
		fatalf(util.LogExec, "%v", err)
	}
	if generated {
		fmt.Fprintf(os.Stderr, "QVOLE_CODE=%s\n", finalCode)
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
	allowAll := fs.Bool("a", false, "Allow ALL peer tunnel requests (not safe)")
	var localTunnels stringSlice
	var remoteTunnels stringSlice
	var allowListen stringSlice
	var allowForward stringSlice
	fs.Var(&localTunnels, "L", "Local tunnel request ([laddr:]lport:raddr:rport)")
	fs.Var(&remoteTunnels, "R", "Remote tunnel request ([raddr:]rport:laddr:lport)")
	fs.Var(&allowListen, "aL", "Allow peer to open a listening port on this addr (allow peer's -R; repeatable)")
	fs.Var(&allowForward, "aF", "Allow peer to connect to the addr:port thru you (allow peer's -L; repeatable)")
	if err := fs.Parse(args); err != nil {
		fatalf(util.LogTunnel, "flag: %v", err)
	}
	util.Debug = debug

	relayAddr := resolveRelay(cf.relay)

	// Fail fast on bad -L/-R specs or allowlist entries before printing a
	// connection code, so the printed code is always accompanied by a
	// runnable configuration.
	if err := validateTunnelConfig(localTunnels, remoteTunnels, allowListen, allowForward, *allowAll); err != nil {
		fatalf(util.LogTunnel, "%v", err)
	}

	finalCode, generated, err := resolveOrGenerateCode(*code)
	if err != nil {
		fatalf(util.LogTunnel, "%v", err)
	}
	if generated {
		fmt.Fprintf(os.Stderr, "QVOLE_CODE=%s\n", finalCode)
		if hint, herr := tunnelPeerCommand(finalCode, localTunnels, remoteTunnels); herr != nil {
			// Specs were validated above, so this should not happen; keep the
			// hint failure visible rather than silently dropping it.
			fmt.Fprintf(os.Stderr, "peer hint unavailable: %v\n", herr)
		} else if hint != "" {
			fmt.Fprintf(os.Stderr, "peer: %s\n", hint)
		}
	}

	ctx, cancel := signalContext()
	defer cancel()

	maybeFatal(util.LogTunnel, app.RunTunnel(ctx, relayAddr, finalCode, localTunnels, remoteTunnels, *allowAll, allowListen, allowForward, 0, 0))
}

func printUsage() {
	fmt.Fprint(os.Stderr, `qvole `+version+`: Fast tunnels over QUIC, burrowed through NATs.

Commands:

  pipe [--stats]                  Bidirectional stream between two peers over QUIC

  tunnel                          TCP port tunneling over P2P QUIC
    -L [laddr:]lport:raddr:rport    ...listen on local port, forward to remote addr:port (peer allows with -aF)
    -R [raddr:]rport:laddr:lport    ...listen on remote port, forward to local addr:port (peer allows with -aL)
    -aL addr:port                   ...allow peer to open a listening port on this addr (allow peer's -R)
    -aF addr:port                   ...allow peer to connect to the addr:port thru you (allow peer's -L)
    -a                              ...allow ALL peer tunnel requests (not safe)

  exec                            Execute a command or connect to an exec peer
    --cmd                           ...run command, pipe stdin/stdout to peer

  relay                           Relay server (UDP)
    --listen <addr>                ...listen address (default :9009)

Common Flags (pipe / exec / tunnel):
  --code CODE                     connection code (or $QVOLE_CODE environment var)
  [--relay addr]                  relay server address (host:port)
  [--debug]                       verbose debug logging to stderr
  [--stats]                       log TX/RX throughput to stderr every 2 s (pipe-only)

Examples:

  # Send a file
  alice$ qvole pipe < in                                          # prints connection code, waits for bob
  bob$   QVOLE_CODE=CODE qvole pipe > out                         # bob connects, receiving file from alice

  # Send a directory
  alice$ tar czf - dir/ | qvole pipe                              # alice streams tar of dir/ to bob
  bob$   QVOLE_CODE=CODE qvole pipe | tar xzf -                   # bob receives and extracts

  # Port Forwarding
  alice$ qvole tunnel -L 8080:localhost:80 -R 2222:localhost:22   # alice configures port forwarding requests
  bob$   QVOLE_CODE=CODE qvole tunnel \
           -aL 127.0.0.1:2222 -aF localhost:80                    # bob allowlists what alice may reach
  alice$ curl localhost:8080                                      # alice can now reach bob's :80
  bob$   ssh -p 2222 localhost                                    # bob can now reach alice's sshd

  # Remote command (alice hosts the command, bob connects)
  alice$ qvole exec --cmd "uptime"                                # command runs on alice, bob sees output
  alice$ qvole exec --cmd "script -q bash /dev/null"              # alice gives bob remote shell
  bob$   QVOLE_CODE=CODE qvole exec

`)
}
