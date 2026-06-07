package engine

import (
	"context"
	"net"
	"testing"
	"time"
)

func newUDPConn(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestListenForPunch_ReceivesFromCorrectPeer(t *testing.T) {
	listener := newUDPConn(t)
	sender := newUDPConn(t)

	peerAddr := sender.LocalAddr().(*net.UDPAddr)
	recvCh := make(chan *net.UDPAddr, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go listenForPunch(ctx, listener, peerAddr, recvCh)

	// give listener time to start
	time.Sleep(50 * time.Millisecond)

	sender.WriteTo([]byte("punch"), listener.LocalAddr())

	select {
	case <-recvCh:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive punch from correct peer")
	}
}

func TestListenForPunch_AcceptsSameIPDifferentPort(t *testing.T) {
	listener := newUDPConn(t)
	correctSender := newUDPConn(t)
	// same IP as correctSender but different port; port check is intentionally relaxed
	// so symmetric-NAT peers can punch from a different port than the relay-reported one.
	wrongSender, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen wrong: %v", err)
	}
	t.Cleanup(func() { wrongSender.Close() })

	peerAddr := correctSender.LocalAddr().(*net.UDPAddr)
	recvCh := make(chan *net.UDPAddr, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go listenForPunch(ctx, listener, peerAddr, recvCh)

	time.Sleep(50 * time.Millisecond)

	// send from wrong port but same IP; should be accepted under relaxed port check
	wrongSender.WriteTo([]byte("wrong"), listener.LocalAddr())

	select {
	case addr := <-recvCh:
		if !addr.IP.Equal(peerAddr.IP) {
			t.Fatalf("expected peer IP %s, got %s", peerAddr.IP, addr.IP)
		}
	case <-ctx.Done():
		t.Fatal("punch from same IP, different port was rejected (expected acceptance)")
	}
}

func TestListenForPunch_NilPeerAddr(t *testing.T) {
	listener := newUDPConn(t)
	sender := newUDPConn(t)

	recvCh := make(chan *net.UDPAddr, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// nil peer addr means accept from anyone
	go listenForPunch(ctx, listener, nil, recvCh)

	time.Sleep(50 * time.Millisecond)

	sender.WriteTo([]byte("punch"), listener.LocalAddr())

	select {
	case <-recvCh:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive punch with nil peer addr")
	}
}

func TestListenForPunch_ContextCancel(t *testing.T) {
	listener := newUDPConn(t)

	recvCh := make(chan *net.UDPAddr, 1)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		listenForPunch(ctx, listener, nil, recvCh)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("listenForPunch did not exit on context cancel")
	}
}

func TestStartHolePunching_SendsPackets(t *testing.T) {
	receiver := newUDPConn(t)
	puncher := newUDPConn(t)

	peerAddr := receiver.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startHolePunching(ctx, puncher, peerAddr)

	// should receive at least one punch packet
	receiver.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1600)
	n, _, err := receiver.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("did not receive punch packet: %v", err)
	}
	if n <= 0 {
		t.Fatal("received empty punch packet")
	}
}

func TestStartHolePunching_ContextCancel(t *testing.T) {
	receiver := newUDPConn(t)
	puncher := newUDPConn(t)

	peerAddr := receiver.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithCancel(context.Background())
	ch := startHolePunching(ctx, puncher, peerAddr)

	// let it send a few packets
	time.Sleep(100 * time.Millisecond)
	cancel()

	// channel should eventually close
	select {
	case _, ok := <-ch:
		if ok {
			// got a success signal, that's fine too
		}
	case <-time.After(3 * time.Second):
		t.Fatal("startHolePunching did not exit after context cancel")
	}
}

func TestStartHolePunching_DetectsPeer(t *testing.T) {
	puncher := newUDPConn(t)
	peer := newUDPConn(t)

	puncherAddr := puncher.LocalAddr().(*net.UDPAddr)
	peerAddr := peer.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// peer receives punch and replies back to puncher
	go func() {
		buf := make([]byte, 1600)
		peer.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _, err := peer.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n > 0 {
			peer.WriteTo([]byte("reply"), puncherAddr)
		}
	}()

	ch := startHolePunching(ctx, puncher, peerAddr)

	select {
	case addr := <-ch:
		if addr == nil {
			t.Fatal("startHolePunching returned nil address")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startHolePunching did not detect peer response")
	}
}

func TestListenForPunch_ZeroLengthPacket(t *testing.T) {
	listener := newUDPConn(t)
	sender := newUDPConn(t)

	recvCh := make(chan *net.UDPAddr, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go listenForPunch(ctx, listener, nil, recvCh)
	time.Sleep(50 * time.Millisecond)

	// Send zero-length packet; listenForPunch should n<=0 continue
	sender.WriteTo([]byte{}, listener.LocalAddr())

	select {
	case <-recvCh:
		t.Fatal("should not receive on zero-length packet")
	case <-ctx.Done():
	}
}

func TestListenForPunch_NonTimeoutReadError(t *testing.T) {
	listener := newUDPConn(t)
	recvCh := make(chan *net.UDPAddr, 1)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		listenForPunch(ctx, listener, nil, recvCh)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	// Close the connection to trigger a non-timeout read error
	listener.Close()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("listenForPunch did not exit after connection close")
	}
}
