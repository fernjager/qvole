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
func RunTunnel(ctx context.Context, relayAddr, code string, localTunnels, remoteTunnels []string, allowTunnel bool) error {
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

	peerAccept, reqs, err := ExchangeTunnelConfig(ctx, conn, myReqs, allowTunnel)
	if err != nil {
		return fmt.Errorf("config exchange: %w", err)
	}
	myReqCount := len(myReqs)

	if len(reqs) == 0 {
		return fmt.Errorf("no tunnel requests exchanged")
	}

	for i, req := range reqs {
		switch {
		case i < myReqCount && !peerAccept:
			util.LogTunnel.PrintfWarn("Peer does not accept tunnels, skipping -%s %s -> %s", req.Type, req.ListenAddr, req.TargetAddr)
		case i >= myReqCount && !allowTunnel:
			util.LogTunnel.PrintfWarn("Use --allow-tunnel to accept peer request -%s %s -> %s", req.Type, req.ListenAddr, req.TargetAddr)
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var acceptorErr, listenerErr error

	wg.Add(1)
	go func() {
		defer func() {
			cancel()
			wg.Done()
		}()
		acceptorErr = RunStreamAcceptor(ctx, conn, reqs, myReqCount, allowTunnel)
	}()

	wg.Add(1)
	go func() {
		defer func() {
			cancel()
			wg.Done()
		}()
		listenerErr = RunTCPListeners(ctx, conn, reqs, myReqCount, peerAccept, allowTunnel)
	}()

	wg.Wait()
	if acceptorErr != nil {
		return acceptorErr
	}
	return listenerErr
}

// ExchangeTunnelConfig exchanges tunnel requests over a QUIC control stream.
// Returns whether the peer accepts tunnels, the combined request list, and any error.
func ExchangeTunnelConfig(ctx context.Context, conn *quic.Conn, myReqs []TunnelRequest, allowTunnel bool) (peerAccept bool, reqs []TunnelRequest, err error) {
	if len(myReqs) > maxTunnelRequests {
		return false, nil, fmt.Errorf("too many tunnel requests: %d (max %d)", len(myReqs), maxTunnelRequests)
	}

	newScanner := func(r io.Reader) *bufio.Scanner {
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
		return s
	}

	if len(myReqs) > 0 {
		stream, err := conn.OpenStreamSync(ctx)
		if err != nil {
			return false, nil, fmt.Errorf("open control stream: %w", err)
		}
		defer stream.Close()

		stream.SetWriteDeadline(time.Now().Add(streamConfigTimeout))
		if err := writeTunnelConfig(stream, allowTunnel, myReqs); err != nil {
			return false, nil, err
		}
		stream.SetWriteDeadline(time.Time{})

		stream.SetReadDeadline(time.Now().Add(streamConfigTimeout))
		scanner := newScanner(stream)
		peerAccept, err = readAcceptLine(scanner)
		if err != nil {
			return false, nil, err
		}
		peerReqs, err := readRequestsFromStream(scanner)
		if err != nil {
			return false, nil, err
		}
		stream.SetReadDeadline(time.Time{})

		reqs = append(myReqs, peerReqs...)
		util.LogTunnel.Printf("Received %d tunnel request(s) from peer (accept=%v)", len(peerReqs), peerAccept)
		return peerAccept, reqs, nil
	}

	stream, err := conn.AcceptStream(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("accept control stream: %w", err)
	}
	defer stream.Close()

	stream.SetReadDeadline(time.Now().Add(streamConfigTimeout))

	scanner := newScanner(stream)
	peerAccept, err = readAcceptLine(scanner)
	if err != nil {
		return false, nil, err
	}
	peerReqs, err := readRequestsFromStream(scanner)
	if err != nil {
		return false, nil, err
	}
	reqs = peerReqs
	util.LogTunnel.Printf("Received %d tunnel request(s) from peer", len(reqs))

	stream.SetWriteDeadline(time.Now().Add(streamConfigTimeout))
	if err := writeTunnelConfig(stream, allowTunnel, myReqs); err != nil {
		return false, nil, err
	}
	stream.SetWriteDeadline(time.Time{})

	reqs = append(reqs, myReqs...)
	util.LogTunnel.PrintfSuccess("Sent %d tunnel request(s)", len(myReqs))
	return peerAccept, reqs, nil
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
// bounded by GetForwardMaxStreams.
func RunStreamAcceptor(ctx context.Context, conn *quic.Conn, reqs []TunnelRequest, myReqCount int, allowTunnel bool) error {
	guard := make(chan struct{}, engine.GetForwardMaxStreams())
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			var appErr *quic.ApplicationError
			if errors.As(err, &appErr) && appErr.ErrorCode == 0 {
				return nil
			}
			return err
		}
		guard <- struct{}{}
		go func(s *quic.Stream) {
			defer func() { <-guard }()
			HandleTunnelStream(ctx, s, reqs, myReqCount, allowTunnel)
		}(stream)
	}
}

// RunTCPListeners starts TCP listeners for local (-L) tunnels I own and remote
// (-R) tunnels the peer owns, forwarding accepted connections over QUIC streams.
func RunTCPListeners(ctx context.Context, conn *quic.Conn, reqs []TunnelRequest, myReqCount int, peerAccept bool, allowTunnel bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, len(reqs))

	for i, req := range reqs {
		myReq := i < myReqCount
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
			if !allowTunnel {
				continue // I must accept peer's tunnels
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
	outboundGuard = make(chan struct{}, engine.GetForwardMaxStreams())
)

// HandleTunnelTCP opens a QUIC stream for a local TCP connection, writing a 2-byte
// header with the tunnel spec index before bridging data.
func HandleTunnelTCP(ctx context.Context, conn *quic.Conn, tcpConn net.Conn, specIdx int) {
	defer tcpConn.Close()

	if specIdx > 65535 {
		util.LogTunnel.PrintfWarn("Spec index %d exceeds uint16 max", specIdx)
		return
	}

	select {
	case outboundGuard <- struct{}{}:
	default:
		util.LogTunnel.PrintfWarn("Outbound stream limit reached, rejecting tunnel %d", specIdx)
		return
	}
	defer func() { <-outboundGuard }()

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
// bridging the TCP connection and QUIC stream bidirectionally.
func HandleTunnelStream(ctx context.Context, stream *quic.Stream, reqs []TunnelRequest, myReqCount int, allowTunnel bool) {
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
	myReq := int(idx) < myReqCount
	if req.Type == "L" && myReq {
		return // I listened for my -L; peer shouldn't open streams for it
	}
	if req.Type == "R" && !myReq {
		return // I listened for peer's -R; peer shouldn't open streams for it
	}

	// Only check allowTunnel for peer-initiated tunnels.
	if !myReq && !allowTunnel {
		util.LogTunnel.PrintfWarn("Tunnel rejected: %s -> %s (use --allow-tunnel)", req.ListenAddr, req.TargetAddr)
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

	util.LogTunnel.PrintfSuccess("Tunnel %d active: ↔ %s", idx, util.Bold(req.TargetAddr))

	// Set an idle deadline on the QUIC stream before the long-lived copy
	// so a peer that goes silent cannot hold the connection open forever.
	stream.SetReadDeadline(time.Now().Add(streamIdleTimeout))
	tcpConn.SetReadDeadline(time.Now().Add(streamIdleTimeout))
	bidirectionalCopy(tcpConn, stream)
}
