package app

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestExchangeTunnelConfig_TooManySpecs(t *testing.T) {
	reqs := make([]TunnelRequest, maxTunnelRequests+1)
	for i := range reqs {
		reqs[i] = TunnelRequest{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"}
	}

	clientConn, _ := setupQUICPair(t)

	_, _, err := ExchangeTunnelConfig(context.Background(), clientConn, reqs, false)
	if err == nil {
		t.Fatal("expected error for too many requests")
	}
	if !strings.Contains(err.Error(), "too many tunnel requests") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExchangeTunnelConfig_InitiatorAndResponder(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	clientReqs := []TunnelRequest{
		{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
	}
	serverReqs := []TunnelRequest{
		{Type: "R", ListenAddr: "127.0.0.1:9022", TargetAddr: "93.184.216.34:22"},
	}

	type result struct {
		reqs       []TunnelRequest
		peerAccept bool
		err        error
		initiator  bool
	}

	clientCh := make(chan result, 1)
	serverCh := make(chan result, 1)

	ctrlStream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("client open stream: %v", err)
	}

	if _, err := fmt.Fprintf(ctrlStream, "ACCEPT true\n"); err != nil {
		t.Fatalf("write accept: %v", err)
	}
	for _, spec := range clientReqs {
		if _, err := fmt.Fprintf(ctrlStream, "%s %s %s\n", spec.Type, spec.ListenAddr, spec.TargetAddr); err != nil {
			t.Fatalf("write spec: %v", err)
		}
	}
	if _, err := fmt.Fprintf(ctrlStream, "END\n"); err != nil {
		t.Fatalf("write end: %v", err)
	}

	go func() {
		stream, err := serverConn.AcceptStream(context.Background())
		if err != nil {
			serverCh <- result{err: err}
			return
		}

		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
		if !scanner.Scan() {
			serverCh <- result{err: fmt.Errorf("read accept: %v", scanner.Err())}
			return
		}
		if scanner.Text() != "ACCEPT true" {
			serverCh <- result{err: fmt.Errorf("expected ACCEPT true, got %q", scanner.Text())}
			return
		}
		var reqs []TunnelRequest
		for scanner.Scan() {
			line := scanner.Text()
			if line == "END" {
				break
			}
			parts := strings.SplitN(line, " ", 3)
			if len(parts) != 3 {
				serverCh <- result{err: fmt.Errorf("invalid spec line: %q", line)}
				return
			}
			reqs = append(reqs, TunnelRequest{Type: parts[0], ListenAddr: parts[1], TargetAddr: parts[2]})
		}
		if err := scanner.Err(); err != nil {
			serverCh <- result{err: err}
			return
		}

		if _, err := fmt.Fprintf(stream, "ACCEPT true\n"); err != nil {
			serverCh <- result{err: err}
			return
		}
		for _, spec := range serverReqs {
			if _, err := fmt.Fprintf(stream, "%s %s %s\n", spec.Type, spec.ListenAddr, spec.TargetAddr); err != nil {
				serverCh <- result{err: err}
				return
			}
		}
		if _, err := fmt.Fprintf(stream, "END\n"); err != nil {
			serverCh <- result{err: err}
			return
		}

		reqs = append(reqs, serverReqs...)
		serverCh <- result{reqs: reqs, peerAccept: true}
	}()

	go func() {
		scanner := bufio.NewScanner(ctrlStream)
		scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)

		if !scanner.Scan() {
			clientCh <- result{err: fmt.Errorf("read accept: %v", scanner.Err())}
			return
		}
		if scanner.Text() != "ACCEPT true" {
			clientCh <- result{err: fmt.Errorf("expected ACCEPT true, got %q", scanner.Text())}
			return
		}
		var reqs []TunnelRequest
		reqs = append(reqs, clientReqs...)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "END" {
				break
			}
			parts := strings.SplitN(line, " ", 3)
			if len(parts) != 3 {
				clientCh <- result{err: fmt.Errorf("invalid spec line: %q", line)}
				return
			}
			reqs = append(reqs, TunnelRequest{Type: parts[0], ListenAddr: parts[1], TargetAddr: parts[2]})
		}
		if err := scanner.Err(); err != nil {
			clientCh <- result{err: err}
			return
		}
		clientCh <- result{reqs: reqs, peerAccept: true, initiator: true}
	}()

	cr := <-clientCh
	sr := <-serverCh

	if cr.err != nil {
		t.Fatalf("client error: %v", cr.err)
	}
	if sr.err != nil {
		t.Fatalf("server error: %v", sr.err)
	}

	if !cr.initiator {
		t.Fatal("client should be initiator")
	}
	if sr.initiator {
		t.Fatal("server should not be initiator")
	}
	if !cr.peerAccept {
		t.Fatal("expected peerAccept=true")
	}
	if !sr.peerAccept {
		t.Fatal("expected peerAccept=true")
	}

	if len(cr.reqs) != 2 {
		t.Fatalf("client got %d requests, want 2", len(cr.reqs))
	}
	if len(sr.reqs) != 2 {
		t.Fatalf("server got %d requests, want 2", len(sr.reqs))
	}

	if cr.reqs[0].Type != "L" || cr.reqs[1].Type != "R" {
		t.Fatalf("client request order: %v", cr.reqs)
	}
	if sr.reqs[0].Type != "L" || sr.reqs[1].Type != "R" {
		t.Fatalf("server request order: %v", sr.reqs)
	}
}

func TestExchangeTunnelConfig_EmptySpecs(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	type result struct {
		reqs       []TunnelRequest
		peerAccept bool
		initiator  bool
		err        error
	}

	clientCh := make(chan result, 1)
	serverCh := make(chan result, 1)

	ctrlStream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("client open stream: %v", err)
	}
	if _, err := fmt.Fprintf(ctrlStream, "ACCEPT true\nEND\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	go func() {
		stream, err := serverConn.AcceptStream(context.Background())
		if err != nil {
			serverCh <- result{err: err}
			return
		}

		scanner := bufio.NewScanner(stream)
		scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
		if !scanner.Scan() {
			serverCh <- result{err: fmt.Errorf("read accept: %v", scanner.Err())}
			return
		}
		accepted := scanner.Text() == "ACCEPT true"
		var reqs []TunnelRequest
		for scanner.Scan() {
			line := scanner.Text()
			if line == "END" {
				break
			}
			reqs = append(reqs, TunnelRequest{})
		}
		if err := scanner.Err(); err != nil {
			serverCh <- result{err: err}
			return
		}

		if _, err := fmt.Fprintf(stream, "ACCEPT true\nEND\n"); err != nil {
			serverCh <- result{err: err}
			return
		}
		serverCh <- result{reqs: reqs, peerAccept: accepted}
	}()

	go func() {
		scanner := bufio.NewScanner(ctrlStream)
		scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
		if !scanner.Scan() {
			clientCh <- result{err: fmt.Errorf("read accept: %v", scanner.Err())}
			return
		}
		accepted := scanner.Text() == "ACCEPT true"
		var reqs []TunnelRequest
		for scanner.Scan() {
			line := scanner.Text()
			if line == "END" {
				break
			}
			reqs = append(reqs, TunnelRequest{})
		}
		if err := scanner.Err(); err != nil {
			clientCh <- result{err: err}
			return
		}
		clientCh <- result{reqs: reqs, peerAccept: accepted, initiator: true}
	}()

	cr := <-clientCh
	sr := <-serverCh

	if cr.err != nil {
		t.Fatalf("client error: %v", cr.err)
	}
	if sr.err != nil {
		t.Fatalf("server error: %v", sr.err)
	}

	if !cr.peerAccept {
		t.Fatal("expected peerAccept=true")
	}
	if !sr.peerAccept {
		t.Fatal("expected peerAccept=true")
	}

	if len(cr.reqs) != 0 {
		t.Fatalf("client got %d requests, want 0", len(cr.reqs))
	}
	if len(sr.reqs) != 0 {
		t.Fatalf("server got %d requests, want 0", len(sr.reqs))
	}
}

func TestExchangeTunnelConfig_MalformedLine(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	ctrlStream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	ctrlStream.Write([]byte("ACCEPT true\n"))
	ctrlStream.Write([]byte("L 127.0.0.1:8080\n"))
	ctrlStream.Write([]byte("END\n"))

	stream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "END" {
			return
		}
		if line == "ACCEPT true" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			return
		}
	}
}

func TestExchangeTunnelConfig_ExceedsScannerBuffer(t *testing.T) {
	exceedMax := make([]byte, scannerMaxTokenSize+1)
	for i := range exceedMax {
		exceedMax[i] = 'x'
	}
	exceedMax[0] = 'L'
	exceedMax[1] = ' '

	clientConn, serverConn := setupQUICPair(t)

	ctrlStream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	ctrlStream.Write([]byte("ACCEPT true\n"))
	ctrlStream.Write(exceedMax)
	ctrlStream.Write([]byte("\n"))
	ctrlStream.Write([]byte("END\n"))

	stream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	buf := make([]byte, 0, scannerMaxTokenSize)
	scanner.Buffer(buf, scannerMaxTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "ACCEPT true" {
			continue
		}
		if len(line) > scannerMaxTokenSize {
			return
		}
		if line == "END" {
			t.Fatal("scanner should have errored on overlong line, but reached END")
		}
	}
	if err := scanner.Err(); err != nil {
		if !strings.Contains(err.Error(), "too long") {
			t.Errorf("unexpected scanner error: %v", err)
		}
		return
	}
	t.Fatal("expected buffer overflow error from scanner")
}

func TestExchangeTunnelConfig_PeerSendsTooManySpecs(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	ctrlStream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	ctrlStream.Write([]byte("ACCEPT true\n"))
	for i := 0; i < maxTunnelRequests*2+1; i++ {
		line := "L 127.0.0.1:8080 8.8.8.8:53\n"
		ctrlStream.Write([]byte(line))
	}
	ctrlStream.Write([]byte("END\n"))

	stream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	defer stream.Close()

	var reqs []TunnelRequest
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "END" {
			break
		}
		if line == "ACCEPT true" {
			continue
		}
		if len(reqs) >= maxTunnelRequests*2 {
			return
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			t.Fatalf("invalid spec line: %q", line)
		}
		reqs = append(reqs, TunnelRequest{
			Type:       parts[0],
			ListenAddr: parts[1],
			TargetAddr: parts[2],
		})
	}
	if len(reqs) >= maxTunnelRequests*2 {
		return
	}
	t.Fatalf("expected to reach maxTunnelRequests*2, only got %d", len(reqs))
}

func TestExchangeTunnelConfig_DirectCall(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	clientReqs := []TunnelRequest{
		{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
	}

	type result struct {
		accept bool
		reqs   []TunnelRequest
		err    error
	}

	clientCh := make(chan result, 1)
	serverCh := make(chan result, 1)

	go func() {
		pa, reqs, err := ExchangeTunnelConfig(context.Background(), clientConn, clientReqs, true)
		clientCh <- result{pa, reqs, err}
	}()

	go func() {
		pa, reqs, err := ExchangeTunnelConfig(context.Background(), serverConn, nil, false)
		serverCh <- result{pa, reqs, err}
	}()

	cr := <-clientCh
	sr := <-serverCh

	if cr.err != nil {
		t.Fatalf("client error: %v", cr.err)
	}
	if sr.err != nil {
		t.Fatalf("server error: %v", sr.err)
	}

	if !sr.accept {
		t.Fatal("expected server peerAccept=true (initiator sent ACCEPT true)")
	}
	if cr.accept {
		t.Fatal("expected client peerAccept=false (responder sent ACCEPT false)")
	}

	if len(cr.reqs) != 1 {
		t.Fatalf("client got %d requests, want 1", len(cr.reqs))
	}
	if len(sr.reqs) != 1 {
		t.Fatalf("server got %d requests, want 1", len(sr.reqs))
	}
	if cr.reqs[0].Type != "L" {
		t.Fatalf("expected type L, got %q", cr.reqs[0].Type)
	}
}

func TestExchangeTunnelConfig_ResponderOnly(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	type result struct {
		accept bool
		reqs   []TunnelRequest
		err    error
	}

	serverCh := make(chan result, 1)
	clientCh := make(chan result, 1)

	go func() {
		pa, reqs, err := ExchangeTunnelConfig(context.Background(), clientConn, []TunnelRequest{
			{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"},
		}, true)
		clientCh <- result{pa, reqs, err}
	}()

	go func() {
		pa, reqs, err := ExchangeTunnelConfig(context.Background(), serverConn, nil, true)
		serverCh <- result{pa, reqs, err}
	}()

	cr := <-clientCh
	sr := <-serverCh

	if cr.err != nil {
		t.Fatalf("client error: %v", cr.err)
	}
	if sr.err != nil {
		t.Fatalf("server error: %v", sr.err)
	}

	if !sr.accept {
		t.Fatal("expected server peerAccept=true")
	}
	if !cr.accept {
		t.Fatal("expected client peerAccept=true")
	}

	if len(cr.reqs) != 1 {
		t.Fatalf("client got %d requests, want 1", len(cr.reqs))
	}
	if len(sr.reqs) != 1 {
		t.Fatalf("server got %d requests, want 1", len(sr.reqs))
	}
	if cr.reqs[0].Type != "L" {
		t.Fatalf("expected type L, got %q", cr.reqs[0].Type)
	}
}

func TestExchangeTunnelConfig_ExactlyMaxTunnelRequests(t *testing.T) {
	clientConn, serverConn := setupQUICPair(t)

	clientReqs := make([]TunnelRequest, maxTunnelRequests)
	for i := range clientReqs {
		clientReqs[i] = TunnelRequest{Type: "L", ListenAddr: "127.0.0.1:8080", TargetAddr: "8.8.8.8:53"}
	}

	ctrlStream, err := clientConn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	ctrlStream.Write([]byte("ACCEPT true\n"))
	for _, spec := range clientReqs {
		line := fmt.Sprintf("%s %s %s\n", spec.Type, spec.ListenAddr, spec.TargetAddr)
		ctrlStream.Write([]byte(line))
	}
	ctrlStream.Write([]byte("END\n"))

	stream, err := serverConn.AcceptStream(context.Background())
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	defer stream.Close()

	var readSpecs int
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, scannerMaxTokenSize), scannerMaxTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "END" {
			break
		}
		if line == "ACCEPT true" {
			continue
		}
		readSpecs++
	}
	if readSpecs != maxTunnelRequests {
		t.Fatalf("expected %d reqs, got %d", maxTunnelRequests, readSpecs)
	}
}
