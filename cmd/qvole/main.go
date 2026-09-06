package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
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

var version = "0.2.2"
var debug bool

func main() {
	log.SetFlags(0)
	_ = os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "1")

	if len(os.Args) < 2 {
		printUsage()
		return
	}

	// -v/--version is only honored in the subcommand position; later
	// occurrences may be flag values (e.g. exec --cmd -v).
	if os.Args[1] == "-v" || os.Args[1] == "--version" {
		fmt.Fprintf(os.Stderr, "qvole %s (protocol v%s)\n", version, engine.ProtocolVersion)
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

// maxTunnelSpecs mirrors maxTunnelRequests in internal/app so an over-long
// -L/-R flag list fails here, before a connection code is printed.
const maxTunnelSpecs = 100

// validateTunnelConfig parses every -L/-R spec and allowlist entry up front so
// typos fail before a connection code is printed. It mirrors the validation
// app.RunTunnel performs, with the same error messages, plus the port-value
// and spec-count checks ParseTunnelRequest does not do.
func validateTunnelConfig(localTunnels, remoteTunnels, allowListen, allowForward []string, allowAll bool) error {
	if n := len(localTunnels) + len(remoteTunnels); n > maxTunnelSpecs {
		return fmt.Errorf("too many tunnel requests: %d (max %d)", n, maxTunnelSpecs)
	}
	for _, f := range localTunnels {
		if err := validateTunnelSpec(f, "L"); err != nil {
			return err
		}
	}
	for _, f := range remoteTunnels {
		if err := validateTunnelSpec(f, "R"); err != nil {
			return err
		}
	}
	return (app.TunnelAllow{All: allowAll, Listen: allowListen, Forward: allowForward}).Validate()
}

// validateTunnelSpec parses one -L/-R spec and validates the port values,
// which ParseTunnelRequest joins into the addresses without interpreting.
func validateTunnelSpec(spec, typ string) error {
	req, err := app.ParseTunnelRequest(spec, typ)
	if err != nil {
		return fmt.Errorf("invalid -%s spec %q: %w", typ, spec, err)
	}
	for _, addr := range []string{req.ListenAddr, req.TargetAddr} {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("invalid -%s spec %q: %v", typ, spec, err)
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("invalid -%s spec %q: invalid port %q", typ, spec, port)
		}
	}
	return nil
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

// peerHintBlock formats the peer's ready-to-paste command. The comment line
// keeps the block safe to copy-paste into a shell.
func peerHintBlock(peerCmd string) string {
	return "# peer command\n" + peerCmd + "\n"
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
	if err != nil && !errors.Is(err, context.Canceled) {
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
		fmt.Fprint(os.Stderr, peerHintBlock("QVOLE_CODE="+finalCode+" qvole pipe"))
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
		fmt.Fprint(os.Stderr, peerHintBlock("QVOLE_CODE="+finalCode+" qvole exec"))
	}

	ctx, cancel := signalContext()
	defer cancel()

	err = app.RunExec(ctx, relayAddr, finalCode, *cmdFlag, *cmdFlag != "")
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := childExitStatus(ctx, exitErr); code != 0 {
			return exitCodeError{code: code}
		}
		return nil
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fatalf(util.LogExec, "%v", err)
	}
	return nil
}

// exitCodeInterrupt is the conventional shell status for SIGINT (128+SIGINT).
const exitCodeInterrupt = 130

// childExitStatus converts a child process exit error into qvole's exit code.
// A child killed by a signal (ExitCode -1) means the run was canceled, by
// SIGINT locally or by the peer closing the stream, which is a clean
// shutdown, not a failure: report 130 for a local interrupt and 0 for a
// peer-driven end, instead of the 255 that os.Exit(-1) produces.
func childExitStatus(ctx context.Context, exitErr *exec.ExitError) int {
	if code := exitErr.ExitCode(); code >= 0 {
		return code
	}
	if ctx.Err() != nil {
		return exitCodeInterrupt
	}
	return 0
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
		if hint, herr := tunnelPeerCommand(finalCode, localTunnels, remoteTunnels); herr != nil {
			// Specs were validated above, so this should not happen; keep the
			// hint failure visible rather than silently dropping it.
			fmt.Fprintf(os.Stderr, "peer hint unavailable: %v\n", herr)
		} else {
			if hint == "" {
				// No -L/-R requests: we're the pure allow side, so suggest the
				// base command for the peer to attach their own flags to.
				hint = "QVOLE_CODE=" + finalCode + " qvole tunnel"
			}
			fmt.Fprint(os.Stderr, peerHintBlock(hint))
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
