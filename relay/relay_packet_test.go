package relay

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func setupRelayConns(t *testing.T) (relayConn *net.UDPConn, clientConn *net.UDPConn, clientAddr *net.UDPAddr) {
	t.Helper()
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()
	relayAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	relayConn, err = net.ListenUDP("udp", relayAddr)
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	clientConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		relayConn.Close()
		t.Fatalf("listen client: %v", err)
	}
	clientAddr = clientConn.LocalAddr().(*net.UDPAddr)
	t.Cleanup(func() {
		relayConn.Close()
		clientConn.Close()
	})
	return relayConn, clientConn, clientAddr
}

// registerClient performs the full two-step REG cookie handshake.
func registerClient(t *testing.T, relayConn, clientConn *net.UDPConn, clientAddr *net.UDPAddr, room string) {
	t.Helper()
	HandlePacket(relayConn, []byte("REG "+room+"\n"), clientAddr)
	regLine := readUDPLine(t, clientConn, 500*time.Millisecond)
	if !strings.HasPrefix(regLine, "REGD "+room+" ") {
		t.Fatalf("expected REGD cookie challenge, got %q", regLine)
	}
	cookie := strings.TrimPrefix(regLine, "REGD "+room+" ")
	HandlePacket(relayConn, []byte("REG "+room+" "+cookie+"\n"), clientAddr)
	regLine2 := readUDPLine(t, clientConn, 500*time.Millisecond)
	if !strings.HasPrefix(regLine2, "REGD "+room+" OK ") {
		t.Fatalf("expected REGD OK confirmation, got %q", regLine2)
	}
}

func uniqueRoom(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func readUDPLine(t *testing.T, conn *net.UDPConn, timeout time.Duration) string {
	t.Helper()
	s, ok := readUDPLineOrNone(t, conn, timeout)
	if !ok {
		t.Fatalf("read: timeout")
	}
	return s
}

func readUDPLineOrNone(t *testing.T, conn *net.UDPConn, timeout time.Duration) (string, bool) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(buf[:n])), true
}

func TestHandlePacket_EmptyData(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	HandlePacket(relayConn, []byte{}, clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for empty data")
	}
}

func TestHandlePacket_TooLarge(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	big := make([]byte, maxDatagramLen+1)
	HandlePacket(relayConn, big, clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for oversized data")
	}
}

func TestHandlePacket_TooShort(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	HandlePacket(relayConn, []byte("ab"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for short data")
	}
}

func TestHandlePacket_UnknownCommand(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	HandlePacket(relayConn, []byte("UNKNOWN foo\n"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for unknown command")
	}
}

func TestHandleReg_NewRoom(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("reg-new")
	registerClient(t, relayConn, clientConn, clientAddr, room)
	// verify room exists
	shard := shardFor(room)
	shard.mu.RLock()
	entry := shard.rooms[room]
	shard.mu.RUnlock()
	if entry == nil {
		t.Fatal("room not created")
	}
	entry.mu.Lock()
	if _, ok := entry.udpClients[clientAddr.String()]; !ok {
		t.Fatal("client not registered")
	}
	entry.mu.Unlock()
}

func TestHandleReg_EmptyRoom(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	HandlePacket(relayConn, []byte("REG  \n"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for empty room")
	}
}

func TestHandleReg_InvalidRoomName(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	HandlePacket(relayConn, []byte("REG room\x00bad\n"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for invalid room name")
	}
}

func TestHandleReg_ReRegistration(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("reg-rereg")
	registerClient(t, relayConn, clientConn, clientAddr, room)
	// Re-reg (no cookie; should re-issue challenge with same cookie).
	HandlePacket(relayConn, []byte("REG "+room+"\n"), clientAddr)
	resp := readUDPLine(t, clientConn, 500*time.Millisecond)
	expectPrefix := "REGD " + room + " "
	if !strings.HasPrefix(resp, expectPrefix) {
		t.Fatalf("expected REGD on re-reg, got %q", resp)
	}
}

func TestHandleMsg_ForwardToOtherClient(t *testing.T) {
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("msg-fwd")
	registerClient(t, relayConn, client1Conn, client1Addr, room)
	registerClient(t, relayConn, client2Conn, client2Addr, room)

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 deadbeef\n"), client1Addr)

	resp := readUDPLine(t, client2Conn, 500*time.Millisecond)
	if resp != "MSGD spake2 deadbeef" {
		t.Fatalf("expected 'MSGD spake2 deadbeef', got %q", resp)
	}
}

func TestHandleMsg_SenderDoesNotReceiveOwnMessage(t *testing.T) {
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("msg-noecho")

	registerClient(t, relayConn, client1Conn, client1Addr, room)
	registerClient(t, relayConn, client2Conn, client2Addr, room)

	HandlePacket(relayConn, []byte("MSG "+room+" confirm aabb\n"), client1Addr)

	client1Conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err = client1Conn.Read(buf)
	if err == nil {
		t.Fatal("sender should not receive own message")
	}
}

func TestHandleMsg_BroadcastToMultipleClients(t *testing.T) {
	resetIPCounts()
	relayConn, senderConn, senderAddr := setupRelayConns(t)

	receiver1Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen receiver1: %v", err)
	}
	defer receiver1Conn.Close()
	receiver1Addr := receiver1Conn.LocalAddr().(*net.UDPAddr)

	receiver2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen receiver2: %v", err)
	}
	defer receiver2Conn.Close()
	receiver2Addr := receiver2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("msg-bcast3")
	registerClient(t, relayConn, senderConn, senderAddr, room)
	registerClient(t, relayConn, receiver1Conn, receiver1Addr, room)
	registerClient(t, relayConn, receiver2Conn, receiver2Addr, room)

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 beef3c01\n"), senderAddr)

	resp1 := readUDPLine(t, receiver1Conn, 500*time.Millisecond)
	if resp1 != "MSGD spake2 beef3c01" {
		t.Fatalf("receiver1 expected 'MSGD spake2 beef3c01', got %q", resp1)
	}

	resp2 := readUDPLine(t, receiver2Conn, 500*time.Millisecond)
	if resp2 != "MSGD spake2 beef3c01" {
		t.Fatalf("receiver2 expected 'MSGD spake2 beef3c01', got %q", resp2)
	}

	senderConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err = senderConn.Read(buf)
	if err == nil {
		t.Fatal("sender should not receive own message")
	}
}

func TestRemoveStaleClients_OnReg(t *testing.T) {
	resetIPCounts()
	relayConn, connA, addrA := setupRelayConns(t)

	connB, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen B: %v", err)
	}
	defer connB.Close()
	addrB := connB.LocalAddr().(*net.UDPAddr)

	connC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen C: %v", err)
	}
	defer connC.Close()
	addrC := connC.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("stale-reg")
	registerClient(t, relayConn, connA, addrA, room)
	registerClient(t, relayConn, connB, addrB, room)

	shard := shardFor(room)
	shard.mu.RLock()
	entry := shard.rooms[room]
	shard.mu.RUnlock()
	entry.mu.Lock()
	entry.udpClients[addrA.String()].lastSeen = time.Now().Add(-2 * regTTL)
	entry.mu.Unlock()

	registerClient(t, relayConn, connC, addrC, room)

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 deadbeef\n"), addrB)

	resp := readUDPLine(t, connC, 500*time.Millisecond)
	if resp != "MSGD spake2 deadbeef" {
		t.Fatalf("client C expected MSGD, got %q", resp)
	}

	connA.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err = connA.Read(buf)
	if err == nil {
		t.Fatal("evicted client A should not receive messages")
	}
}

func TestReReg_DoesNotBumpRoomTTL(t *testing.T) {
	resetIPCounts()
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("rereg-ttl")

	registerClient(t, relayConn, clientConn, clientAddr, room)

	shard := shardFor(room)
	shard.mu.RLock()
	entry := shard.rooms[room]
	shard.mu.RUnlock()
	entry.mu.Lock()
	origT := entry.t
	entry.mu.Unlock()

	time.Sleep(10 * time.Millisecond)
	HandlePacket(relayConn, []byte("REG "+room+"\n"), clientAddr)
	readUDPLine(t, clientConn, 500*time.Millisecond)

	entry.mu.Lock()
	newT := entry.t
	entry.mu.Unlock()

	if newT.After(origT.Add(5 * time.Millisecond)) {
		t.Fatalf("entry.t was bumped: orig=%v new=%v", origT, newT)
	}
}

func TestHandleMsg_InvalidFormat(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("msg-invalid")

	registerClient(t, relayConn, clientConn, clientAddr, room)

	HandlePacket(relayConn, []byte("MSG "+room+"\n"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for invalid MSG format")
	}
}

func TestHandleMsg_InvalidHex(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("msg-hex")

	registerClient(t, relayConn, clientConn, clientAddr, room)

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 zzzz\n"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for invalid hex")
	}
}

func TestHandleMsg_RateLimiting(t *testing.T) {
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("msg-rate")

	registerClient(t, relayConn, client1Conn, client1Addr, room)
	registerClient(t, relayConn, client2Conn, client2Addr, room)

	for i := 0; i < maxMsgRate; i++ {
		HandlePacket(relayConn, []byte(fmt.Sprintf("MSG %s spake2 %02x\n", room, i)), client1Addr)
		readUDPLine(t, client2Conn, 500*time.Millisecond)
	}

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 ff\n"), client1Addr)
	client2Conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err = client2Conn.Read(buf)
	if err == nil {
		t.Fatal("expected rate limited message to be dropped")
	}
}

func TestHandleMsg_UnknownRoom(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)

	HandlePacket(relayConn, []byte("MSG unknown-room spake2 aabb\n"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for unknown room")
	}
}

func TestHandleMsg_UnknownSender(t *testing.T) {
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("msg-unknown-sender")

	HandlePacket(relayConn, []byte("REG "+room+"\n"), client1Addr)
	readUDPLine(t, client1Conn, 500*time.Millisecond)

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 aabb\n"), client2Addr)
	client2Conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err = client2Conn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for unregistered sender")
	}
}

func TestHandleMsg_UnknownPhase(t *testing.T) {
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("msg-unknown-phase")

	registerClient(t, relayConn, client1Conn, client1Addr, room)
	registerClient(t, relayConn, client2Conn, client2Addr, room)

	HandlePacket(relayConn, []byte("MSG "+room+" badphase aabb\n"), client1Addr)
	client2Conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err = client2Conn.Read(buf)
	if err == nil {
		t.Fatal("expected no MSGD for unknown phase")
	}
}

func TestHandleMsg_KnownPhasesForwarded(t *testing.T) {
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("msg-known-phase")

	registerClient(t, relayConn, client1Conn, client1Addr, room)
	registerClient(t, relayConn, client2Conn, client2Addr, room)

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 aabb\n"), client1Addr)
	msg := readUDPLine(t, client2Conn, 500*time.Millisecond)
	if !strings.HasPrefix(msg, "MSGD spake2 ") {
		t.Fatalf("expected MSGD spake2 prefix, got %q", msg)
	}

	HandlePacket(relayConn, []byte("MSG "+room+" confirm aabb\n"), client1Addr)
	msg = readUDPLine(t, client2Conn, 500*time.Millisecond)
	if !strings.HasPrefix(msg, "MSGD confirm ") {
		t.Fatalf("expected MSGD confirm prefix, got %q", msg)
	}
}

func TestCleanupRegs_EvictsStaleRooms(t *testing.T) {
	room := uniqueRoom("cleanup-test")
	shard := shardFor(room)

	shard.mu.Lock()
	entry := &roomEntry{
		udpClients: map[string]*udpClient{
			"127.0.0.1:12345": {
				lastSeen:     time.Now().Add(-2 * regTTL),
				rateLimiter:  rateLimiter{windowStart: time.Now().Add(-2 * regTTL)},
				resolvedAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
			},
		},
		t: time.Now().Add(-2 * regTTL),
	}
	shard.rooms[room] = entry
	shard.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				close(done)
				return
			case <-ticker.C:
				for i := range shards {
					sh := &shards[i]
					sh.mu.Lock()
					for r, e := range sh.rooms {
						e.mu.Lock()
						for addr, info := range e.udpClients {
							if time.Since(info.lastSeen) > regTTL {
								delete(e.udpClients, addr)
							}
						}
						if len(e.udpClients) == 0 && time.Since(e.t) > regTTL {
							delete(sh.rooms, r)
						}
						e.mu.Unlock()
					}
					sh.mu.Unlock()
				}
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	shard.mu.RLock()
	_, exists := shard.rooms[room]
	shard.mu.RUnlock()
	if exists {
		t.Fatal("stale room should have been evicted")
	}
}

func TestCleanupRegs_KeepsActiveRooms(t *testing.T) {
	room := uniqueRoom("cleanup-active")
	shard := shardFor(room)

	shard.mu.Lock()
	entry := &roomEntry{
		udpClients: map[string]*udpClient{
			"127.0.0.1:12345": {
				lastSeen:     time.Now(),
				rateLimiter:  rateLimiter{windowStart: time.Now()},
				resolvedAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
			},
		},
		t: time.Now(),
	}
	shard.rooms[room] = entry
	shard.mu.Unlock()

	defer func() {
		shard.mu.Lock()
		delete(shard.rooms, room)
		shard.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for i := range shards {
					sh := &shards[i]
					sh.mu.Lock()
					for r, e := range sh.rooms {
						e.mu.Lock()
						for addr, info := range e.udpClients {
							if time.Since(info.lastSeen) > regTTL {
								delete(e.udpClients, addr)
							}
						}
						if len(e.udpClients) == 0 && time.Since(e.t) > regTTL {
							delete(sh.rooms, r)
						}
						e.mu.Unlock()
					}
					sh.mu.Unlock()
				}
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	shard.mu.RLock()
	_, exists := shard.rooms[room]
	shard.mu.RUnlock()
	if !exists {
		t.Fatal("active room should not have been evicted")
	}
}

func TestWriteRelay(t *testing.T) {
	relayAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	relayConn, err := net.ListenUDP("udp", relayAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer relayConn.Close()

	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client: %v", err)
	}
	defer clientConn.Close()
	clientAddr := clientConn.LocalAddr().(*net.UDPAddr)

	msg := []byte("test message\n")
	if err := writeRelay(relayConn, msg, clientAddr); err != nil {
		t.Fatalf("writeRelay: %v", err)
	}

	buf := make([]byte, 1500)
	clientConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != "test message\n" {
		t.Fatalf("got %q, want %q", string(buf[:n]), "test message\n")
	}
}

func TestHandleReg_HardCap(t *testing.T) {
	relayConn, _, _ := setupRelayConns(t)
	room := uniqueRoom("reg-hard-cap")

	oldCap := maxClientsHard
	maxClientsHard = 3
	defer func() { maxClientsHard = oldCap }()

	for i := 0; i < maxClientsHard; i++ {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen client %d: %v", i, err)
		}
		defer c.Close()
		addr := c.LocalAddr().(*net.UDPAddr)
		registerClient(t, relayConn, c, addr, room)
	}

	extraConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen extra: %v", err)
	}
	defer extraConn.Close()
	extraAddr := extraConn.LocalAddr().(*net.UDPAddr)

	HandlePacket(relayConn, []byte("REG "+room+"\n"), extraAddr)
	extraConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err = extraConn.Read(buf)
	if err == nil {
		t.Fatal("expected no REGD when hard cap reached")
	}
}

func TestHandleReg_NoQueuedMessagesOnJoin(t *testing.T) {
	resetIPCounts()
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("reg-noqueue")

	HandlePacket(relayConn, []byte("REG "+room+"\n"), client1Addr)
	readUDPLine(t, client1Conn, 500*time.Millisecond)

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 aabb\n"), client1Addr)

	HandlePacket(relayConn, []byte("REG "+room+"\n"), client2Addr)
	regResp, ok := readUDPLineOrNone(t, client2Conn, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(regResp, "REGD ") {
		t.Fatalf("expected REGD, got %q", regResp)
	}

	_, ok = readUDPLineOrNone(t, client2Conn, 100*time.Millisecond)
	if ok {
		t.Fatal("expected no queued messages (queuing removed)")
	}
}

func TestEvictStaleRooms_DirectCall(t *testing.T) {
	oldRegTTL := regTTL
	regTTL = 10 * time.Millisecond
	defer func() { regTTL = oldRegTTL }()

	room := uniqueRoom("evict-direct")
	shard := shardFor(room)

	shard.mu.Lock()
	shard.rooms[room] = &roomEntry{
		udpClients: map[string]*udpClient{
			"1.2.3.4:5678": {
				lastSeen:     time.Now().Add(-2 * regTTL),
				rateLimiter:  rateLimiter{windowStart: time.Now()},
				resolvedAddr: &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 5678},
			},
		},
		t: time.Now().Add(-2 * regTTL),
	}
	shard.mu.Unlock()
	totalRoomCount.Add(1)

	shard.mu.Lock()
	n := evictStaleRooms(shard)
	shard.mu.Unlock()

	if n == 0 {
		t.Fatal("expected at least one room evicted")
	}

	shard.mu.RLock()
	_, exists := shard.rooms[room]
	shard.mu.RUnlock()
	if exists {
		t.Fatal("stale room should have been evicted")
	}
}

func TestCleanupRegs_LiveRun(t *testing.T) {
	oldRegTTL := regTTL
	oldRegCleanupInterval := regCleanupInterval
	regTTL = 50 * time.Millisecond
	regCleanupInterval = 20 * time.Millisecond
	defer func() {
		regTTL = oldRegTTL
		regCleanupInterval = oldRegCleanupInterval
	}()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		cleanupRegs(ctx)
		close(done)
	}()

	defer func() {
		cancel()
		<-done
	}()

	room := uniqueRoom("cleanup-live")
	shard := shardFor(room)

	shard.mu.Lock()
	shard.rooms[room] = &roomEntry{
		udpClients: map[string]*udpClient{
			"1.2.3.4:5678": {
				lastSeen:     time.Now().Add(-2 * regTTL),
				rateLimiter:  rateLimiter{windowStart: time.Now()},
				resolvedAddr: &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 5678},
			},
		},
		t: time.Now().Add(-2 * regTTL),
	}
	shard.mu.Unlock()
	totalRoomCount.Add(1)

	time.Sleep(150 * time.Millisecond)

	shard.mu.RLock()
	_, exists := shard.rooms[room]
	shard.mu.RUnlock()
	if exists {
		t.Fatal("stale room should have been evicted by cleanup goroutine")
	}
}

func TestHandleReg_PerIPRoomLimit(t *testing.T) {
	relayConn, _, _ := setupRelayConns(t)

	savedCount := totalRoomCount.Load()
	savedMaxMsgRate := maxMsgRate
	maxMsgRate = 1000
	savedShards := make([]map[string]*roomEntry, numShards)
	for i := range shards {
		savedShards[i] = make(map[string]*roomEntry)
		shard := &shards[i]
		shard.mu.Lock()
		for k, v := range shard.rooms {
			savedShards[i][k] = v
		}
		shard.rooms = make(map[string]*roomEntry)
		shard.mu.Unlock()
	}
	totalRoomCount.Store(0)
	resetIPCounts()

	defer func() {
		maxMsgRate = savedMaxMsgRate
		totalRoomCount.Store(savedCount)
		for i := range shards {
			shard := &shards[i]
			shard.mu.Lock()
			shard.rooms = savedShards[i]
			shard.mu.Unlock()
		}
		resetIPCounts()
	}()

	for i := 0; i < maxRoomsPerIP; i++ {
		clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen client %d: %v", i, err)
		}
		defer clientConn.Close()
		clientAddr := clientConn.LocalAddr().(*net.UDPAddr)

		room := uniqueRoom(fmt.Sprintf("ip-limit-%d", i))
		registerClient(t, relayConn, clientConn, clientAddr, room)
	}

	extraConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen extra: %v", err)
	}
	defer extraConn.Close()
	extraAddr := extraConn.LocalAddr().(*net.UDPAddr)

	extraRoom := uniqueRoom("ip-limit-extra")
	HandlePacket(relayConn, []byte("REG "+extraRoom+"\n"), extraAddr)
	_, ok := readUDPLineOrNone(t, extraConn, 100*time.Millisecond)
	if ok {
		t.Fatal("expected no REGD when per-IP room limit reached")
	}
}

func TestPendingReg_DoesNotCreateRoom(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("pending-noroom")

	// Initial REG should get a cookie challenge but NOT create a room.
	HandlePacket(relayConn, []byte("REG "+room+"\n"), clientAddr)
	regLine, ok := readUDPLineOrNone(t, clientConn, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(regLine, "REGD "+room+" ") {
		t.Fatalf("expected REGD cookie challenge, got %q", regLine)
	}

	// Verify no room was created.
	shard := shardFor(room)
	shard.mu.RLock()
	_, exists := shard.rooms[room]
	shard.mu.RUnlock()
	if exists {
		t.Fatal("room should not exist after initial REG (deferred creation)")
	}

	// Verify totalRoomCount did not increase.
	if n := totalRooms(); n != 0 {
		t.Fatalf("totalRooms = %d, want 0", n)
	}

	// Verify pending registration was stored.
	if p := getPendingReg(room, clientAddr.String()); p == nil {
		t.Fatal("pending registration not found in store")
	}
}

func TestPendingReg_CookieCompletionCreatesRoom(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("pending-create")

	// Initial REG.
	HandlePacket(relayConn, []byte("REG "+room+"\n"), clientAddr)
	regLine, ok := readUDPLineOrNone(t, clientConn, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(regLine, "REGD "+room+" ") {
		t.Fatalf("expected REGD cookie challenge, got %q", regLine)
	}
	cookie := strings.TrimPrefix(regLine, "REGD "+room+" ")

	// Cookie completion.
	HandlePacket(relayConn, []byte("REG "+room+" "+cookie+"\n"), clientAddr)
	regLine2, ok := readUDPLineOrNone(t, clientConn, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(regLine2, "REGD "+room+" OK ") {
		t.Fatalf("expected REGD OK, got %q", regLine2)
	}

	// Verify room was created.
	shard := shardFor(room)
	shard.mu.RLock()
	entry := shard.rooms[room]
	shard.mu.RUnlock()
	if entry == nil {
		t.Fatal("room should exist after cookie completion")
	}

	// Verify client was admitted.
	entry.mu.Lock()
	_, admitted := entry.udpClients[clientAddr.String()]
	entry.mu.Unlock()
	if !admitted {
		t.Fatal("client should be admitted after cookie completion")
	}

	// Verify totalRoomCount increased.
	if n := totalRooms(); n != 1 {
		t.Fatalf("totalRooms = %d, want 1", n)
	}

	// Verify pending was removed.
	if p := getPendingReg(room, clientAddr.String()); p != nil {
		t.Fatal("pending registration should have been removed")
	}
}

func TestPendingReg_RoomExhaustion(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	// Save and restore state.
	savedMaxRooms := maxRooms
	savedMaxMsgRate := maxMsgRate
	maxRooms = 10
	maxMsgRate = 1000
	savedCount := totalRoomCount.Load()
	defer func() {
		maxRooms = savedMaxRooms
		maxMsgRate = savedMaxMsgRate
		totalRoomCount.Store(savedCount)
		resetRooms()
		resetIPCounts()
	}()

	relayConn, _, _ := setupRelayConns(t)

	// Flood initial REGs from different IPs (each on a distinct local port,
	// but all from 127.0.0.1). None should create rooms.
	for i := 0; i < maxRooms*3; i++ {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen client %d: %v", i, err)
		}
		addr := c.LocalAddr().(*net.UDPAddr)
		room := uniqueRoom(fmt.Sprintf("exhaust-%d", i))
		HandlePacket(relayConn, []byte("REG "+room+"\n"), addr)
		// Drain the response.
		_, _ = readUDPLineOrNone(t, c, 100*time.Millisecond)
		c.Close()
	}

	// Verify that no rooms were created (room table not exhausted).
	if n := totalRooms(); n != 0 {
		t.Fatalf("totalRooms = %d, want 0 (rooms should not be created on initial REG)", n)
	}
}

func TestCookieCompletion_RechecksHardCap(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	oldCap := maxClientsHard
	maxClientsHard = 3
	defer func() { maxClientsHard = oldCap }()

	relayConn, _, _ := setupRelayConns(t)
	room := uniqueRoom("cookie-hardcap")

	// Admit one client to create the room.
	c1, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen c1: %v", err)
	}
	defer c1.Close()
	addr1 := c1.LocalAddr().(*net.UDPAddr)
	registerClient(t, relayConn, c1, addr1, room)

	// Collect cookies for 3 more clients while the room is under the hard cap.
	type pendingClient struct {
		conn   *net.UDPConn
		addr   *net.UDPAddr
		cookie string
	}
	var extras []pendingClient
	for i := 0; i < 3; i++ {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen extra %d: %v", i, err)
		}
		defer c.Close()
		addr := c.LocalAddr().(*net.UDPAddr)

		HandlePacket(relayConn, []byte("REG "+room+"\n"), addr)
		line, ok := readUDPLineOrNone(t, c, 500*time.Millisecond)
		if !ok || !strings.HasPrefix(line, "REGD "+room+" ") {
			t.Fatalf("extra %d: expected REGD cookie, got %q", i, line)
		}
		extras = append(extras, pendingClient{
			conn:   c,
			addr:   addr,
			cookie: strings.TrimPrefix(line, "REGD "+room+" "),
		})
	}

	// Now complete the cookies. The first two should succeed (filling the cap
	// to 3), the third should be rejected by the re-check.
	admitted := 0
	for i, pc := range extras {
		HandlePacket(relayConn, []byte("REG "+room+" "+pc.cookie+"\n"), pc.addr)
		line, ok := readUDPLineOrNone(t, pc.conn, 500*time.Millisecond)
		if ok && strings.HasPrefix(line, "REGD "+room+" OK ") {
			admitted++
		} else if admitted < maxClientsHard-1 {
			t.Fatalf("extra %d: expected admission (admitted=%d, cap=%d)", i, admitted, maxClientsHard)
		}
	}

	// With the re-check, at most (maxClientsHard - 1) of the extras should
	// be admitted (plus the initial client = maxClientsHard total).
	if admitted > maxClientsHard-1 {
		t.Fatalf("admitted %d extras, want at most %d (hard cap re-check failed)", admitted, maxClientsHard-1)
	}
}

func TestCookieCompletion_RechecksPerIPCap(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	savedMaxRoomsPerIP := maxRoomsPerIP
	maxRoomsPerIP = 2
	defer func() { maxRoomsPerIP = savedMaxRoomsPerIP }()

	relayConn, _, _ := setupRelayConns(t)

	// Collect cookies for 3 rooms via the pending store (rooms don't exist
	// yet, so the initial REG goes to pending, not blocked by per-IP cap).
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen c: %v", err)
	}
	defer c.Close()
	addr := c.LocalAddr().(*net.UDPAddr)

	type cookieInfo struct {
		room   string
		cookie string
	}
	var cookies []cookieInfo
	for i := 0; i < 3; i++ {
		room := uniqueRoom(fmt.Sprintf("ipcap-%d", i))
		HandlePacket(relayConn, []byte("REG "+room+"\n"), addr)
		line, ok := readUDPLineOrNone(t, c, 500*time.Millisecond)
		if !ok || !strings.HasPrefix(line, "REGD "+room+" ") {
			t.Fatalf("room %d: expected REGD cookie, got %q", i, line)
		}
		cookies = append(cookies, cookieInfo{
			room:   room,
			cookie: strings.TrimPrefix(line, "REGD "+room+" "),
		})
	}

	// Complete first two cookies; both should succeed (IP count goes 0→1→2).
	for i := 0; i < 2; i++ {
		ci := cookies[i]
		HandlePacket(relayConn, []byte("REG "+ci.room+" "+ci.cookie+"\n"), addr)
		line, ok := readUDPLineOrNone(t, c, 500*time.Millisecond)
		if !ok || !strings.HasPrefix(line, "REGD "+ci.room+" OK ") {
			t.Fatalf("room %d: expected REGD OK, got %q", i, line)
		}
	}

	// Complete third cookie; should be rejected because IP is at cap (2).
	ci := cookies[2]
	HandlePacket(relayConn, []byte("REG "+ci.room+" "+ci.cookie+"\n"), addr)
	_, ok := readUDPLineOrNone(t, c, 100*time.Millisecond)
	if ok {
		t.Fatal("expected third cookie completion to be rejected at per-IP cap")
	}
}

func TestCookieCompletion_RechecksPerIPCap_ExistingRoom(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	savedMaxRoomsPerIP := maxRoomsPerIP
	maxRoomsPerIP = 2
	defer func() { maxRoomsPerIP = savedMaxRoomsPerIP }()

	relayConn, caConn, caAddr := setupRelayConns(t)

	// Client B is a distinct (unroutable) source IP; responses to it are not
	// observed, so its cookies are read from the pending store instead.
	fakeIP := "10.99.99.99"
	bAddr := &net.UDPAddr{IP: net.ParseIP(fakeIP), Port: 4242}
	bSrc := bAddr.String()

	roomX := uniqueRoom("ipcapx")
	roomY := uniqueRoom("ipcapy")
	roomZ := uniqueRoom("ipcapz")

	// B pre-registers X, Y, Z via the pending store (none exist yet).
	var cookies []string
	for _, room := range []string{roomX, roomY, roomZ} {
		HandlePacket(relayConn, []byte("REG "+room+"\n"), bAddr)
		p := getPendingReg(room, bSrc)
		if p == nil {
			t.Fatalf("room %s: expected pending registration for %s", room, bSrc)
		}
		cookies = append(cookies, hex.EncodeToString(p.cookie))
	}

	// Client A (127.0.0.1) creates room X via the pending store.
	registerClient(t, relayConn, caConn, caAddr, roomX)

	// B completes Y and Z; both create rooms, putting B's IP at cap (2).
	for i, room := range []string{roomY, roomZ} {
		HandlePacket(relayConn, []byte("REG "+room+" "+cookies[i+1]+"\n"), bAddr)
		if n := countIPRooms(fakeIP); n != i+1 {
			t.Fatalf("after completing %s: countIPRooms(%s) = %d, want %d", room, fakeIP, n, i+1)
		}
	}

	// B completes X; room exists (created by A), B's IP is at cap and not in
	// the room, so the exists-branch per-IP re-check must reject it.
	HandlePacket(relayConn, []byte("REG "+roomX+" "+cookies[0]+"\n"), bAddr)
	if n := countIPRooms(fakeIP); n != maxRoomsPerIP {
		t.Fatalf("countIPRooms(%s) = %d after rejected completion, want %d (per-IP cap bypassed)", fakeIP, n, maxRoomsPerIP)
	}
	sh := shardFor(roomX)
	sh.mu.RLock()
	entry, ok := sh.rooms[roomX]
	sh.mu.RUnlock()
	if !ok {
		t.Fatalf("room %s disappeared", roomX)
	}
	entry.mu.Lock()
	_, admitted := entry.udpClients[bSrc]
	nClients := len(entry.udpClients)
	entry.mu.Unlock()
	if admitted {
		t.Fatalf("client %s was admitted into existing room %s despite per-IP cap", bSrc, roomX)
	}

	// Room X must remain usable: client C (same IP as A, already in room X)
	// completes its cookie and is admitted.
	ccConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen cc: %v", err)
	}
	defer ccConn.Close()
	ccAddr := ccConn.LocalAddr().(*net.UDPAddr)
	HandlePacket(relayConn, []byte("REG "+roomX+"\n"), ccAddr)
	lineC, ok := readUDPLineOrNone(t, ccConn, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(lineC, "REGD "+roomX+" ") {
		t.Fatalf("client C: expected REGD cookie, got %q", lineC)
	}
	cookieC := strings.TrimPrefix(lineC, "REGD "+roomX+" ")
	HandlePacket(relayConn, []byte("REG "+roomX+" "+cookieC+"\n"), ccAddr)
	lineC2, ok := readUDPLineOrNone(t, ccConn, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(lineC2, "REGD "+roomX+" OK ") {
		t.Fatalf("client C: expected REGD OK, got %q", lineC2)
	}
	entry.mu.Lock()
	after := len(entry.udpClients)
	entry.mu.Unlock()
	if after != nClients+1 {
		t.Fatalf("room %s has %d clients after C joined, want %d", roomX, after, nClients+1)
	}
}

func TestCookieReg_UnknownRoom_Rechallenged(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	relayConn, c, addr := setupRelayConns(t)

	room := uniqueRoom("rechal")
	// Initial REG → pending store + challenge.
	HandlePacket(relayConn, []byte("REG "+room+"\n"), addr)
	line1, ok := readUDPLineOrNone(t, c, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(line1, "REGD "+room+" ") {
		t.Fatalf("expected REGD cookie, got %q", line1)
	}
	cookie1 := strings.TrimPrefix(line1, "REGD "+room+" ")

	// Evict the pending entry, simulating pendingRegTTL expiry.
	removeStalePendingRegs(time.Now().Add(time.Hour))

	// Cookie REG for the now-unknown room must be re-challenged, not dropped.
	HandlePacket(relayConn, []byte("REG "+room+" "+cookie1+"\n"), addr)
	line2, ok := readUDPLineOrNone(t, c, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(line2, "REGD "+room+" ") {
		t.Fatalf("expected re-challenge for unknown room, got %q", line2)
	}
	if strings.HasPrefix(line2, "REGD "+room+" OK ") {
		t.Fatal("expected a fresh cookie challenge, not admission")
	}
	cookie2 := strings.TrimPrefix(line2, "REGD "+room+" ")

	// Completing with the fresh cookie creates the room and admits the client.
	HandlePacket(relayConn, []byte("REG "+room+" "+cookie2+"\n"), addr)
	line3, ok := readUDPLineOrNone(t, c, 500*time.Millisecond)
	if !ok || !strings.HasPrefix(line3, "REGD "+room+" OK ") {
		t.Fatalf("expected REGD OK after re-challenge, got %q", line3)
	}
}

func TestPendingReg_Cleanup(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	oldTTL := regTTL
	oldPendingTTL := pendingRegTTL
	regTTL = 50 * time.Millisecond
	// We can't change the const, but we can test that stale pending is removed
	// by directly manipulating the timestamps.
	defer func() {
		regTTL = oldTTL
		_ = oldPendingTTL
	}()

	room := uniqueRoom("pending-cleanup")
	sh := pendingShardFor(room)

	// Store a pending registration with an old timestamp.
	storePendingReg(room, "1.2.3.4:5678", &pendingReg{
		cookie:       []byte("deadbeefdeadbeef"),
		createdAt:    time.Now().Add(-2 * pendingRegTTL),
		resolvedAddr: &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 5678},
	})

	// Verify it exists.
	if p := getPendingReg(room, "1.2.3.4:5678"); p == nil {
		t.Fatal("pending registration should exist before cleanup")
	}

	// Run cleanup.
	removeStalePendingRegs(time.Now())

	// Verify it was removed.
	sh.mu.Lock()
	_, exists := sh.regs[room]
	sh.mu.Unlock()
	if exists {
		t.Fatal("stale pending registration should have been evicted")
	}
}

func TestPendingReg_PerIPLimit(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	savedMaxPendingPerIP := maxPendingPerIP
	maxPendingPerIP = 2
	defer func() { maxPendingPerIP = savedMaxPendingPerIP }()

	relayConn, _, _ := setupRelayConns(t)

	// Fill pending per-IP limit.
	for i := 0; i < maxPendingPerIP; i++ {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Fatalf("listen client %d: %v", i, err)
		}
		addr := c.LocalAddr().(*net.UDPAddr)
		room := uniqueRoom(fmt.Sprintf("pend-limit-%d", i))
		HandlePacket(relayConn, []byte("REG "+room+"\n"), addr)
		_, _ = readUDPLineOrNone(t, c, 100*time.Millisecond)
		c.Close()
	}

	// Third pending should be rejected.
	extraConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen extra: %v", err)
	}
	defer extraConn.Close()
	extraAddr := extraConn.LocalAddr().(*net.UDPAddr)

	extraRoom := uniqueRoom("pend-limit-extra")
	HandlePacket(relayConn, []byte("REG "+extraRoom+"\n"), extraAddr)
	_, ok := readUDPLineOrNone(t, extraConn, 100*time.Millisecond)
	if ok {
		t.Fatal("expected no REGD when pending per-IP limit reached")
	}
}
