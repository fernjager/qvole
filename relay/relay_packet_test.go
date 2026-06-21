package relay

import (
	"context"
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
	// Re-reg (no cookie — should re-issue challenge with same cookie).
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
