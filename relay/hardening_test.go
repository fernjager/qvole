package relay

import (
	"fmt"
	"net"
	"testing"
	"time"
)

// TestInRoomPendingHasSeparateBudget verifies that pending registrations for
// an existing room are bounded by their own per-room/global budget, not by the
// pre-cookie store's budget. Before this was enforced, an attacker could flood
// REGs for one room from many (spoofed) source addresses and grow
// entry.pending without bound. Sharing the pre-cookie budget made that same
// flood freeze new-room registration relay-wide.
func TestInRoomPendingHasSeparateBudget(t *testing.T) {
	resetRegRateLimits()
	resetIPCounts()
	resetRooms()

	savedGlobal := maxPendingGlobal
	savedInRoom := maxInRoomPending
	savedRegRate := maxRegRate
	maxPendingGlobal = 3
	maxInRoomPending = 3
	maxRegRate = 1000
	defer func() {
		maxPendingGlobal = savedGlobal
		maxInRoomPending = savedInRoom
		maxRegRate = savedRegRate
		resetRooms()
		resetIPCounts()
	}()

	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("inroom-budget")

	// Create the room through a legitimate cookie handshake.
	registerClient(t, relayConn, clientConn, clientAddr, room)

	// Flood the existing room from distinct source addresses. No cookie is
	// needed to reach the in-room pending map, so these are the same packets a
	// spoofing attacker would send.
	for i := 0; i < 20; i++ {
		src := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 35000 + i}
		HandlePacket(relayConn, []byte("REG "+room+"\n"), src)
	}

	sh := shardFor(room)
	sh.mu.RLock()
	entry := sh.rooms[room]
	sh.mu.RUnlock()
	if entry == nil {
		t.Fatal("room disappeared")
	}

	entry.mu.Lock()
	n := len(entry.pending)
	entry.mu.Unlock()

	if n > maxInRoomPending {
		t.Fatalf("in-room pending = %d, want <= in-room budget %d", n, maxInRoomPending)
	}
	if got := inRoomPendingTotal.Load(); got != int64(n) {
		t.Fatalf("inRoomPendingTotal = %d, want %d", got, n)
	}
	if n != maxInRoomPending {
		t.Fatalf("in-room pending = %d, want exactly %d accepted before the cap", n, maxInRoomPending)
	}

	// The pre-cookie store has its own budget, so a new room can still be
	// registered even while one room's pending map is full. This allocation
	// must succeed (the separate budget prevents a relay-wide freeze).
	newRoom := uniqueRoom("inroom-budget-new")
	freshSrc := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 36000}
	HandlePacket(relayConn, []byte("REG "+newRoom+"\n"), freshSrc)
	psh := pendingShardFor(newRoom)
	psh.mu.Lock()
	_, allocated := psh.regs[newRoom]
	psh.mu.Unlock()
	if !allocated {
		t.Fatal("new-room REG was denied after only the in-room pending budget filled")
	}
}

// TestDropLogLimiterBounded verifies the global lines/sec budget and the hard
// map cap that prevent a spoofed-source (or IPv6 /64) flood from turning every
// packet into a log line and a map entry.
func TestDropLogLimiterBounded(t *testing.T) {
	// Global lines/second budget.
	l := &ipRateLimiter{maxPerSec: 2}
	allowed := 0
	for i := 0; i < 10; i++ {
		if l.allow(fmt.Sprintf("10.0.0.%d", i), 0) {
			allowed++
		}
	}
	if allowed != l.maxPerSec {
		t.Fatalf("allowed %d log lines in one window, want %d", allowed, l.maxPerSec)
	}

	// Hard map cap, even with a high global budget.
	l = &ipRateLimiter{maxPerSec: 1000, maxEntries: 3}
	allowed = 0
	for i := 0; i < 10; i++ {
		if l.allow(fmt.Sprintf("11.0.0.%d", i), 0) {
			allowed++
		}
	}
	if allowed != l.maxEntries {
		t.Fatalf("allowed %d entries with map cap %d", allowed, l.maxEntries)
	}
	if len(l.last) != l.maxEntries {
		t.Fatalf("rate-limiter map has %d entries, want cap %d", len(l.last), l.maxEntries)
	}
}

func TestPerPrefixClientCap(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)
	room := uniqueRoom("prefix-client-cap")

	saved := maxClientsPerPrefixPerRoom
	maxClientsPerPrefixPerRoom = 2
	defer func() { maxClientsPerPrefixPerRoom = saved }()

	// Two clients from the same prefix are the expected peer pair.
	registerClient(t, relayConn, clientConn, clientAddr, room)
	c2, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen c2: %v", err)
	}
	defer c2.Close()
	registerClient(t, relayConn, c2, c2.LocalAddr().(*net.UDPAddr), room)

	// A third source from the same prefix must not be able to fill the room.
	c3, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen c3: %v", err)
	}
	defer c3.Close()
	HandlePacket(relayConn, []byte("REG "+room+"\n"), c3.LocalAddr().(*net.UDPAddr))
	if line, ok := readUDPLineOrNone(t, c3, 150*time.Millisecond); ok {
		t.Fatalf("third client from the same prefix was admitted: %q", line)
	}

	sh := shardFor(room)
	sh.mu.RLock()
	entry := sh.rooms[room]
	sh.mu.RUnlock()
	entry.mu.Lock()
	n := len(entry.udpClients)
	entry.mu.Unlock()
	if n != 2 {
		t.Fatalf("room has %d clients, want 2 (per-prefix cap)", n)
	}
}
