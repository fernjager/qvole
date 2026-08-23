package app

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
)

// RunTunnel establishes a peer-to-peer tunnel, exchanging port-forwarding configurations
// and starting TCP listeners for local tunnels and stream acceptors for remote tunnels.
// allowAll (-a) accepts every peer request; otherwise allowListen (-aL) and
// allowForward (-aF) allowlist which peer tunnel requests are honored
// (see TunnelAllow). An empty allowlist accepts nothing.
// forwardMaxStreams overrides QVOLE_FORWARD_MAX_STREAMS; 0 means use env/default.
func RunTunnel(ctx context.Context, relayAddr, code string, localTunnels, remoteTunnels []string, allowAll bool, allowListen, allowForward []string, forwardMaxStreams int) error {
	allow := TunnelAllow{All: allowAll, Listen: allowListen, Forward: allowForward}
	if err := allow.Validate(); err != nil {
		return err
	}

	conn, isServer, err := engine.ConnectPeer(ctx, relayAddr, code)
	if err != nil {
		return err
	}
	defer conn.CloseWithError(0, "done")

	role := util.RoleString(isServer)
	util.LogTunnel.PrintfSuccess("Connected as %s", role)

	var myReqs []TunnelRequest
	var s *TunnelRequest
	for _, f := range localTunnels {
		s, err = ParseTunnelRequest(f, "L")
		if err != nil {
			return fmt.Errorf("invalid -L spec %q: %w", f, err)
		}
		myReqs = append(myReqs, *s)
	}
	for _, f := range remoteTunnels {
		s, err = ParseTunnelRequest(f, "R")
		if err != nil {
			return fmt.Errorf("invalid -R spec %q: %w", f, err)
		}
		myReqs = append(myReqs, *s)
	}

	peerAccept, reqs, myReqStart, err := ExchangeTunnelConfig(ctx, conn, myReqs, allow, isServer)
	if err != nil {
		return fmt.Errorf("config exchange: %w", err)
	}
	myReqCount := len(myReqs)
	mine := func(i int) bool { return i >= myReqStart && i < myReqStart+myReqCount }

	if len(reqs) == 0 {
		return fmt.Errorf("no tunnel requests exchanged")
	}

	for i, req := range reqs {
		my := mine(i)
		switch {
		case my && !peerAccept:
			// Peer has no allowlist (-a/-aL/-aF); tell the operator what the
			// peer needs to run to accept each of our requests.
			switch req.Type {
			case "L":
				util.LogTunnel.PrintfWarn("Peer rejected -L %s -> %s (peer must run with -aF %s to allow)", req.ListenAddr, req.TargetAddr, req.TargetAddr)
			case "R":
				util.LogTunnel.PrintfWarn("Peer rejected -R %s -> %s (peer must run with -aL %s to allow)", req.ListenAddr, req.TargetAddr, req.ListenAddr)
			}
		case !my && !allow.Accepts():
			util.LogTunnel.PrintfWarn("Ignoring peer request -%s %s -> %s (run with -aL/-aF to accept, or -a to allow all)", req.Type, req.ListenAddr, req.TargetAddr)
		case my && req.Type == "R" && peerAccept:
			// The peer listens on our -R address; we dial the target for each
			// inbound connection.
			util.LogTunnel.PrintfSuccess("Tunnel forwarding to %s", util.Bold(req.TargetAddr))
		case !my && req.Type == "L" && allow.AllowsForward(req.TargetAddr):
			// The peer's -L: they listen; we dial their target for each
			// inbound connection.
			util.LogTunnel.PrintfSuccess("Tunnel forwarding to %s", util.Bold(req.TargetAddr))
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Resolve the forward max streams limit (zero means use env/default).
	fms := forwardMaxStreams
	if fms <= 0 {
		fms = engine.GetForwardMaxStreams()
	}
	initOutboundGuard(fms)

	// Per-session announcement state: each RunTunnel announces "Tunnel N
	// established" once per spec, independent of other sessions.
	announced := &sync.Map{}

	var wg sync.WaitGroup
	var acceptorErr, listenerErr error

	wg.Add(1)
	go func() {
		defer func() {
			cancel()
			wg.Done()
		}()
		acceptorErr = RunStreamAcceptor(ctx, conn, reqs, myReqStart, myReqCount, allow, fms, announced)
	}()

	wg.Add(1)
	go func() {
		defer func() {
			cancel()
			wg.Done()
		}()
		listenerErr = RunTCPListeners(ctx, conn, reqs, myReqStart, myReqCount, peerAccept, allow)
	}()

	wg.Wait()
	// A failure on one side cancels the context, which makes the other side
	// report context.Canceled; prefer the real error so it is not shadowed
	// and silently swallowed by the caller.
	for _, err := range []error{acceptorErr, listenerErr} {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	if acceptorErr != nil {
		return acceptorErr
	}
	return listenerErr
}

// logSentConfig logs what this side sent in the tunnel config exchange: the
// number of requests, or that it is merely awaiting the peer's requests when
// it configured none.
func logSentConfig(myReqs []TunnelRequest) {
	if len(myReqs) == 0 {
		util.LogTunnel.PrintfInfo("Awaiting tunnel request(s) from peer")
		return
	}
	util.LogTunnel.PrintfSuccess("Sent %d tunnel request(s)", len(myReqs))
}

// ExchangeTunnelConfig exchanges tunnel requests over a QUIC control stream.
// Returns whether the peer accepts the request set, the combined request list,
// the index of this side's first request within the combined list, and any
// error. Each side declares ACCEPT true when it is willing to accept requests
// (-a, or a non-empty -aL/-aF allowlist); individual requests are still
// filtered against the allowlist locally.
//
// The client opens the control stream and the server accepts it, which is
// deterministic on both sides. Both sides write their config on the stream
// and read the peer's config from the same stream; reads and writes are
// bounded by streamConfigTimeout so a stalled peer cannot hold the exchange
// open indefinitely.
//
// Both sides order the combined request list identically: the client's
// requests come first, then the server's. The returned myReqStart is the index
// of this side's requests within that canonical ordering (0 for the client,
// len(peerReqs) for the server), so stream-header indexes map to the same
// request on both peers.
func ExchangeTunnelConfig(ctx context.Context, conn *quic.Conn, myReqs []TunnelRequest, allow TunnelAllow, isServer bool) (peerAccept bool, reqs []TunnelRequest, myReqStart int, err error) {
	if len(myReqs) > maxTunnelRequests {
		return false, nil, 0, fmt.Errorf("too many tunnel requests: %d (max %d)", len(myReqs), maxTunnelRequests)
	}

	newScanner := func(r io.Reader) *bufio.Scanner {
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
		return s
	}

	var stream *quic.Stream
	if !isServer {
		// Client: open the control stream.
		s, err := conn.OpenStreamSync(ctx)
		if err != nil {
			return false, nil, 0, fmt.Errorf("open control stream: %w", err)
		}
		stream = s
	} else {
		// Server: accept the client's stream, bounded so a peer that never
		// opens one cannot stall the exchange.
		acceptCtx, cancel := context.WithTimeout(ctx, streamConfigTimeout)
		defer cancel()
		s, err := conn.AcceptStream(acceptCtx)
		if err != nil {
			return false, nil, 0, fmt.Errorf("accept control stream: %w", err)
		}
		stream = s
	}
	defer stream.Close()

	// Each side writes its config, then reads the peer's from the same stream.
	stream.SetWriteDeadline(time.Now().Add(streamConfigTimeout))
	if err := writeTunnelConfig(stream, allow.Accepts(), myReqs); err != nil {
		return false, nil, 0, err
	}
	stream.SetWriteDeadline(time.Time{})
	logSentConfig(myReqs)

	stream.SetReadDeadline(time.Now().Add(streamConfigTimeout))
	scanner := newScanner(stream)
	peerAccept, err = readAcceptLine(scanner)
	if err != nil {
		return false, nil, 0, err
	}
	peerReqs, err := readRequestsFromStream(scanner)
	if err != nil {
		return false, nil, 0, err
	}
	stream.SetReadDeadline(time.Time{})

	// Canonical ordering: the client's requests first, then the server's.
	if isServer {
		reqs = append(peerReqs, myReqs...)
		myReqStart = len(peerReqs)
	} else {
		reqs = append(myReqs, peerReqs...)
	}
	util.LogTunnel.PrintfInfo("Received %d tunnel request(s) from peer", len(peerReqs))
	return peerAccept, reqs, myReqStart, nil
}

// readAcceptLine reads the ACCEPT line from a scanner and returns the boolean value.
func readAcceptLine(scanner *bufio.Scanner) (bool, error) {
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, fmt.Errorf("read accept: %w", err)
		}
		return false, fmt.Errorf("read accept: unexpected EOF")
	}
	switch scanner.Text() {
	case "ACCEPT true":
		return true, nil
	case "ACCEPT false":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected line: %q", scanner.Text())
	}
}

// writeTunnelConfig writes the ACCEPT line, tunnel requests, and END to a writer.
func writeTunnelConfig(w io.Writer, allow bool, reqs []TunnelRequest) error {
	if _, err := fmt.Fprintf(w, "ACCEPT %t\n", allow); err != nil {
		return fmt.Errorf("write accept: %w", err)
	}
	for i, req := range reqs {
		if _, err := fmt.Fprintf(w, "%s %s %s\n", req.Type, req.ListenAddr, req.TargetAddr); err != nil {
			return fmt.Errorf("write request %d: %w", i, err)
		}
	}
	if _, err := fmt.Fprintf(w, "END\n"); err != nil {
		return fmt.Errorf("write end: %w", err)
	}
	return nil
}

// RunStreamAcceptor accepts inbound QUIC streams and dispatches them to HandleTunnelStream,
// bounded by forwardMaxStreams. myReqStart/myReqCount describe the index range
// of this side's requests within reqs (see ExchangeTunnelConfig); announced is
// the per-session announcement map (see announceTunnelOnce).
func RunStreamAcceptor(ctx context.Context, conn *quic.Conn, reqs []TunnelRequest, myReqStart, myReqCount int, allow TunnelAllow, forwardMaxStreams int, announced *sync.Map) error {
	guard := make(chan struct{}, forwardMaxStreams)
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			// Any remote close (graceful, application error 0, or abrupt)
			// ends the session: report it as a disconnect rather than failing
			// silently or dumping a raw quic-go error.
			if engine.LogDisconnect(util.LogTunnel, err) {
				return nil
			}
			return err
		}
		guard <- struct{}{}
		go func(s *quic.Stream) {
			defer func() { <-guard }()
			HandleTunnelStream(ctx, s, reqs, myReqStart, myReqCount, allow, announced)
		}(stream)
	}
}

// RunTCPListeners starts TCP listeners for local (-L) tunnels I own and remote
// (-R) tunnels the peer owns, forwarding accepted connections over QUIC streams.
// Peer -R requests are honored only when their listen address matches an -aL
// allowlist entry (or -a is set). myReqStart/myReqCount describe the index range
// of this side's requests within reqs (see ExchangeTunnelConfig).
func RunTCPListeners(ctx context.Context, conn *quic.Conn, reqs []TunnelRequest, myReqStart, myReqCount int, peerAccept bool, allow TunnelAllow) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, len(reqs))

	for i, req := range reqs {
		myReq := i >= myReqStart && i < myReqStart+myReqCount
		if myReq {
			if req.Type == "R" {
				continue // -R means peer should listen, not me
			}
			if !peerAccept {
				continue // peer must accept my tunnels
			}
		} else {
			if req.Type == "L" {
				continue // -L means peer should listen, not me
			}
			if !allow.AllowsListen(req.ListenAddr) {
				util.LogTunnel.PrintfWarn("Ignoring peer request -R %s -> %s (add -aL %s to allow)", req.ListenAddr, req.TargetAddr, req.ListenAddr)
				continue
			}
		}

		listener, err := net.Listen("tcp", req.ListenAddr)
		if err != nil {
			return fmt.Errorf("listen on %s: %w", req.ListenAddr, err)
		}
		defer listener.Close()

		idx := i
		s := req
		go func() {
			<-ctx.Done()
			listener.Close()
		}()
		go func() {
			util.LogTunnel.PrintfSuccess("Tunnel listening on %s (%s -> %s)", util.Bold(s.ListenAddr), s.Type, util.Bold(s.TargetAddr))
			for {
				tcpConn, err := listener.Accept()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					cancel()
					select {
					case errs <- fmt.Errorf("accept on %s: %w", s.ListenAddr, err):
					default:
					}
					return
				}
				util.LogTunnel.Printf("New connection on %s", s.ListenAddr)
				go HandleTunnelTCP(ctx, conn, tcpConn, idx)
			}
		}()
	}

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	outboundGuard     chan struct{}
	outboundGuardOnce sync.Once
)

func initOutboundGuard(n int) {
	outboundGuardOnce.Do(func() {
		outboundGuard = make(chan struct{}, n)
	})
}

// getOutboundGuard returns the outbound semaphore channel, initializing it
// lazily from GetForwardMaxStreams if initOutboundGuard was not called.
func getOutboundGuard() chan struct{} {
	initOutboundGuard(engine.GetForwardMaxStreams())
	return outboundGuard
}

// announceTunnelOnce logs a one-time success message the first time a connection
// is established through tunnel spec idx within a tunnel session, so users get a
// clear "tunnel works" confirmation without per-connection noise. The map is
// per-RunTunnel (not global) so repeated or concurrent sessions announce
// independently. It returns true for the call that performed the announcement.
func announceTunnelOnce(announced *sync.Map, idx int, target string) bool {
	onceIface, _ := announced.LoadOrStore(idx, &sync.Once{})
	first := false
	once := onceIface.(*sync.Once)
	once.Do(func() {
		first = true
		util.LogTunnel.PrintfSuccess("Tunnel established: ↔ %s", util.Bold(target))
	})
	return first
}

// HandleTunnelTCP opens a QUIC stream for a local TCP connection, writing a 2-byte
// header with the tunnel spec index before bridging data.
func HandleTunnelTCP(ctx context.Context, conn *quic.Conn, tcpConn net.Conn, specIdx int) {
	defer tcpConn.Close()

	if specIdx > 65535 {
		util.LogTunnel.PrintfWarn("Spec index %d exceeds uint16 max", specIdx)
		return
	}

	select {
	case getOutboundGuard() <- struct{}{}:
	default:
		util.LogTunnel.PrintfWarn("Outbound stream limit reached, rejecting tunnel %d", specIdx)
		return
	}
	defer func() { <-getOutboundGuard() }()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		util.LogTunnel.PrintfError("Opening stream for tunnel %d failed: %v", specIdx, err)
		return
	}

	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(specIdx))
	if _, err := stream.Write(header[:]); err != nil {
		stream.Close()
		util.LogTunnel.PrintfError("Writing header for tunnel %d failed: %v", specIdx, err)
		return
	}

	tcpConn.SetReadDeadline(time.Now().Add(streamIdleTimeout))
	bidirectionalCopy(tcpConn, stream)
	stream.Close()
}

// HandleTunnelStream reads the stream header and dials the corresponding tunnel target,
// bridging the TCP connection and QUIC stream bidirectionally. myReqStart/myReqCount
// describe the index range of this side's requests within reqs (see
// ExchangeTunnelConfig); announced is the per-session announcement map (see
// announceTunnelOnce).
func HandleTunnelStream(ctx context.Context, stream *quic.Stream, reqs []TunnelRequest, myReqStart, myReqCount int, allow TunnelAllow, announced *sync.Map) {
	defer stream.Close()

	stream.SetReadDeadline(time.Now().Add(streamHeaderTimeout))
	var header [2]byte
	if _, err := io.ReadFull(stream, header[:]); err != nil {
		return
	}
	stream.SetReadDeadline(time.Time{})
	idx := binary.BigEndian.Uint16(header[:])
	if int(idx) >= len(reqs) {
		util.LogTunnel.PrintfWarn("Invalid tunnel index %d (max %d)", idx, len(reqs)-1)
		return
	}

	req := reqs[idx]

	// Dial for peer's -L (they listened) or my -R (they listened on my behalf).
	myReq := int(idx) >= myReqStart && int(idx) < myReqStart+myReqCount
	if req.Type == "L" && myReq {
		return // I listened for my -L; peer shouldn't open streams for it
	}
	if req.Type == "R" && !myReq {
		return // I listened for peer's -R; peer shouldn't open streams for it
	}

	// Allowlist check: peer-initiated tunnels are honored only when the target
	// matches an -aF entry (or -a is set). Peer -R requests returned earlier,
	// so only peer -L requests reach this point.
	if !myReq && !allow.AllowsForward(req.TargetAddr) {
		util.LogTunnel.PrintfWarn("Tunnel rejected: %s -> %s (add -aF %s to allow)", req.ListenAddr, req.TargetAddr, req.TargetAddr)
		return
	}

	dialTimeoutVal := util.EnvDuration("QVOLE_DIAL_TIMEOUT_MS", dialTimeout)
	d := net.Dialer{Timeout: dialTimeoutVal}
	tcpConn, err := d.DialContext(ctx, "tcp", req.TargetAddr)
	if err != nil {
		util.LogTunnel.PrintfError("Connecting to %s for tunnel %d failed: %v", util.Bold(req.TargetAddr), idx, err)
		return
	}
	defer tcpConn.Close()

	// Announce the first successful connection through this tunnel spec once;
	// later connections only log at debug level so busy tunnels don't spam.
	announceTunnelOnce(announced, int(idx), req.TargetAddr)
	util.LogTunnel.Printf("Tunnel %d connection: ↔ %s", idx, util.Bold(req.TargetAddr))

	// Set an absolute 5-minute lifetime cap on the QUIC stream so a peer
	// that goes silent cannot hold the connection open indefinitely.
	// Note: this is not a refreshing idle deadline; active streams also
	// hit this limit.
	stream.SetReadDeadline(time.Now().Add(streamIdleTimeout))
	tcpConn.SetReadDeadline(time.Now().Add(streamIdleTimeout))
	bidirectionalCopy(tcpConn, stream)
}
