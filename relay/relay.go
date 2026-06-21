package relay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/fernjager/qvole/internal/util"
)

const dropLogInterval = time.Second

const (
	readBufSize          = 1500
	cmdPrefixLen         = 4
	unknownTypeMaxLogLen = 40
	maxMSGBodyLen        = 1024 // hex chars (512 bytes), covers SPAKE2 + confirm

	defaultPktChanBuf         = 256
	defaultWritePoolSize      = 256
	defaultRelayStatsInterval = 5 * time.Minute
	defaultWriteDeadline      = 500 * time.Millisecond
)

var (
	pktChanBuf         int           = defaultPktChanBuf
	writePoolSize      int           = defaultWritePoolSize
	relayStatsInterval time.Duration = defaultRelayStatsInterval
	writeDeadline      time.Duration = defaultWriteDeadline

	readPool sync.Pool

	// writePool is a bounded semaphore that limits concurrent outbound
	// writes. When non-nil (production), writes are dispatched to
	// background goroutines so slow targets cannot pin relay workers.
	// When nil (tests), writes execute directly on the calling goroutine.
	writePool chan struct{}

	// writeMu serializes SetWriteDeadline + WriteTo pairs on the shared
	// conn, eliminating the race between concurrent workers.
	writeMu sync.Mutex
)

func init() {
	pktChanBuf = util.EnvInt("QVOLE_RELAY_PKT_CHAN_BUF", defaultPktChanBuf)
	writePoolSize = util.EnvInt("QVOLE_RELAY_WRITE_POOL", defaultWritePoolSize)
	relayStatsInterval = util.EnvDuration("QVOLE_RELAY_STATS_INTERVAL_MS", defaultRelayStatsInterval)
	writeDeadline = util.EnvDuration("QVOLE_RELAY_WRITE_DEADLINE_MS", defaultWriteDeadline)
	readPool.New = func() any { return make([]byte, readBufSize) }
}

// RunRelay starts the UDP relay server, listening on addr and processing
// REG/MSG/REGD/MSGD protocol messages from clients.
func RunRelay(ctx context.Context, addr string) error {
	go cleanupRegs(ctx)

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve UDP: %w", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen UDP: %w", err)
	}
	defer conn.Close()

	writePool = make(chan struct{}, writePoolSize)

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	pktCh := make(chan pkt, pktChanBuf)
	for i := 0; i < relayWorkers; i++ {
		go func() {
			for p := range pktCh {
				HandlePacket(conn, p.data, p.src)
			}
		}()
	}

	util.LogRelay.PrintfSuccess("Listening on UDP %s (%d workers, %d writePool)", util.Bold(addr), relayWorkers, writePoolSize)

	go func() {
		ticker := time.NewTicker(relayStatsInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				util.LogRelay.Printf("Stats: %d rooms, %d clients | %d REGs, %d MSGs relayed, %d drops", totalRooms(), totalClients(), statRegs.Load(), statMsgs.Load(), statDrops.Load())
			}
		}
	}()

	buf := make([]byte, readBufSize)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				close(pktCh)
				return nil
			default:
			}
			util.LogRelay.PrintfError("UDP read error: %v", err)
			close(pktCh)
			return err
		}
		// Use a pool buffer to reduce per-packet heap allocations.
		data := readPool.Get().([]byte)
		copy(data[:n], buf[:n])
		select {
		case pktCh <- pkt{data: data[:n], src: src}:
		default:
			readPool.Put(data)
			statDrops.Add(1)
			util.LogRelay.PrintfWarn("Packet dropped from %s: worker channel full", src)
		}
	}
}

// --- UDP listener ---

type pkt struct {
	data []byte
	src  *net.UDPAddr
}

// HandlePacket dispatches a raw inbound datagram to the appropriate REG/MSG handler.
func HandlePacket(conn *net.UDPConn, data []byte, src *net.UDPAddr) {
	if len(data) == 0 || len(data) > maxDatagramLen {
		dropPacket(src, "Dropped oversized datagram (%d bytes) from %s", len(data), src)
		return
	}
	trimmed := bytes.TrimSpace(data)
	regPrefix := []byte("REG ")
	msgPrefix := []byte("MSG ")
	switch {
	case len(trimmed) >= cmdPrefixLen && bytes.HasPrefix(trimmed, regPrefix):
		regParts := bytes.SplitN(bytes.TrimSpace(trimmed[cmdPrefixLen:]), []byte(" "), 2)
		room := string(regParts[0])
		var cookie []byte
		if len(regParts) > 1 {
			cookie = bytes.TrimSpace(regParts[1])
		}
		if len(room) == 0 {
			dropPacket(src, "Dropped REG with empty room from %s", src)
			return
		}
		if !validRoomName(room) {
			dropPacket(src, "Dropped REG with invalid room %q from %s", room, src)
			return
		}
		handleReg(conn, room, cookie, src)
	case len(trimmed) >= cmdPrefixLen && bytes.HasPrefix(trimmed, msgPrefix):
		payload := trimmed[cmdPrefixLen:]
		handleMsg(conn, payload, src)
	default:
		end := len(trimmed)
		if end > unknownTypeMaxLogLen {
			end = unknownTypeMaxLogLen
		}
		dropPacket(src, "Dropped unknown packet type from %s: %q", src, string(trimmed[:end]))
	}
}

// writeRelay sends msg to addr via conn. In production (writePool != nil),
// writes are dispatched to a bounded goroutine pool with a per-conn mutex
// serialising SetWriteDeadline + WriteTo. This prevents slow targets from
// blocking relay workers (worker starvation) and eliminates the data race
// between concurrent SetWriteDeadline calls (undefined behaviour).
func writeRelay(conn *net.UDPConn, msg []byte, addr *net.UDPAddr) error {
	if writePool != nil {
		select {
		case writePool <- struct{}{}:
		default:
			statDrops.Add(1)
			return nil
		}
		go func() {
			defer func() { <-writePool }()
			writeMu.Lock()
			conn.SetWriteDeadline(time.Now().Add(writeDeadline))
			_, err := conn.WriteTo(msg, addr)
			writeMu.Unlock()
			if err != nil {
				util.LogRelay.PrintfWarn("UDP write to %s failed: %v", addr, err)
			}
		}()
		return nil
	}
	// Direct write (tests — writePool is nil).
	writeMu.Lock()
	conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := conn.WriteTo(msg, addr)
	writeMu.Unlock()
	return err
}

const (
	pendingRegTTL      = 5 * time.Second
	pendingCookieBytes = 16
)

func sendREGD(conn *net.UDPConn, room string, src *net.UDPAddr) {
	if err := writeRelay(conn, []byte(fmt.Sprintf("REGD %s OK %s\n", room, src.String())), src); err != nil {
		util.LogRelay.PrintfWarn("REGD write to %s failed: %v", src, err)
	}
}

func sendRegChallenge(conn *net.UDPConn, room string, cookie []byte, src *net.UDPAddr) {
	if err := writeRelay(conn, []byte(fmt.Sprintf("REGD %s %x\n", room, cookie)), src); err != nil {
		util.LogRelay.PrintfWarn("REGD challenge write to %s failed: %v", src, err)
	}
}

// dropPacket always increments the drop counter and rate-limits the log line
// per source IP so a flood of invalid packets cannot overwhelm logs.
func dropPacket(src *net.UDPAddr, format string, args ...any) {
	statDrops.Add(1)
	if !dropLogLimiter.allow(ipKey(src.String()), dropLogInterval) {
		return
	}
	util.LogRelay.PrintfWarn(format, args...)
}

// --- UDP handlers ---

func handleReg(conn *net.UDPConn, room string, cookie []byte, src *net.UDPAddr) {
	srcStr := src.String()
	srcIP := ipKey(srcStr)

	// If a cookie is present, this is the follow-up REG — verify it
	// against the pending entry to prove return routability.
	if len(cookie) > 0 {
		if !isValidHex(string(cookie)) || len(cookie) != pendingCookieBytes*2 {
			dropPacket(src, "Dropped REG with invalid cookie from %s", srcStr)
			return
		}
		cookieBytes, err := hex.DecodeString(string(cookie))
		if err != nil {
			dropPacket(src, "Dropped REG with invalid cookie hex from %s", srcStr)
			return
		}
		shard := shardFor(room)
		shard.mu.RLock()
		entry, ok := shard.rooms[room]
		shard.mu.RUnlock()
		if !ok {
			dropPacket(src, "Dropped REG cookie for unknown room %s from %s", room, srcStr)
			return
		}
		entry.mu.Lock()
		pend, exists := entry.pending[srcStr]
		if !exists || !bytes.Equal(pend.cookie, cookieBytes) {
			entry.mu.Unlock()
			dropPacket(src, "Dropped REG with mismatched cookie from %s in room %s", srcStr, room)
			return
		}
		// Cookie matches — admit the client.
		delete(entry.pending, srcStr)
		u := &udpClient{
			lastSeen:     time.Now(),
			rateLimiter:  rateLimiter{windowStart: time.Now()},
			resolvedAddr: pend.resolvedAddr,
		}
		entry.udpClients[srcStr] = u
		entry.t = time.Now()

		myIP := srcIP
		ipAlreadyInRoom := false
		for addr := range entry.udpClients {
			if addr != srcStr && ipKey(addr) == myIP {
				ipAlreadyInRoom = true
				break
			}
		}
		if !ipAlreadyInRoom {
			addIPRoom(myIP, room)
		}
		statRegs.Add(1)
		util.LogRelay.Printf("Client %s joined room %s", srcStr, room)
		entry.mu.Unlock()

		sendREGD(conn, room, src)
		return
	}

	// No cookie: initial REG.
	if isRegRateLimited(srcIP) {
		dropPacket(src, "Rate-limited REG from %s", srcStr)
		return
	}

	if countIPRooms(srcIP) >= maxRoomsPerIP {
		dropPacket(src, "Room limit per IP reached for %s", srcStr)
		return
	}

	shard := shardFor(room)

	shard.mu.Lock()
	entry, ok := shard.rooms[room]
	if !ok {
		if totalRooms() >= maxRooms {
			if n := evictStaleRooms(shard); n > 0 {
				util.LogRelay.PrintfWarn("Evicted %d stale room(s)", n)
			}
		}
		if totalRooms() >= maxRooms {
			shard.mu.Unlock()
			dropPacket(src, "Room %s rejected: max rooms reached (room)", room)
			return
		}
		if countIPRooms(srcIP) >= maxRoomsPerIP {
			shard.mu.Unlock()
			dropPacket(src, "Room limit per IP reached for %s (re-check)", srcStr)
			return
		}
		// Generate a cookie for this pending registration.
		cb := make([]byte, pendingCookieBytes)
		if _, err := rand.Read(cb); err != nil {
			shard.mu.Unlock()
			util.LogRelay.PrintfWarn("rand cookie failed: %v", err)
			return
		}
		entry = &roomEntry{
			udpClients: make(map[string]*udpClient),
			pending: map[string]*pendingReg{
				srcStr: {cookie: cb, createdAt: time.Now(), resolvedAddr: src},
			},
			t: time.Now(),
		}
		shard.rooms[room] = entry
		totalRoomCount.Add(1)
		shard.mu.Unlock()
		sendRegChallenge(conn, room, cb, src)
		return
	}
	entry.mu.Lock()
	shard.mu.Unlock()

	entry.removeStaleClients(room, time.Now())

	// Check hard cap (must also account for pending entries).
	if len(entry.udpClients) >= maxClientsHard && entry.udpClients[srcStr] == nil {
		entry.mu.Unlock()
		dropPacket(src, "Room %s at hard cap (%d), rejecting %s", room, maxClientsHard, srcStr)
		return
	}

	// Re-registration of an already-admitted client.
	if entry.udpClients[srcStr] != nil {
		entry.udpClients[srcStr].lastSeen = time.Now()
		entry.udpClients[srcStr].resolvedAddr = src
		entry.mu.Unlock()
		statRegs.Add(1)
		sendREGD(conn, room, src)
		return
	}

	// Check for existing pending registration — re-issue the same cookie.
	if pend, exists := entry.pending[srcStr]; exists {
		pend.createdAt = time.Now()
		pend.resolvedAddr = src
		cb := pend.cookie
		entry.mu.Unlock()
		sendRegChallenge(conn, room, cb, src)
		return
	}

	// Fresh pending registration.
	cb := make([]byte, pendingCookieBytes)
	if _, err := rand.Read(cb); err != nil {
		entry.mu.Unlock()
		util.LogRelay.PrintfWarn("rand cookie failed: %v", err)
		return
	}
	if entry.pending == nil {
		entry.pending = make(map[string]*pendingReg)
	}
	entry.pending[srcStr] = &pendingReg{cookie: cb, createdAt: time.Now(), resolvedAddr: src}
	entry.mu.Unlock()
	sendRegChallenge(conn, room, cb, src)
}

func handleMsg(conn *net.UDPConn, payload []byte, src *net.UDPAddr) {
	parts := bytes.SplitN(payload, []byte(" "), 3)
	if len(parts) != 3 {
		dropPacket(src, "Dropped malformed MSG from %s", src)
		return
	}
	room, phase, body := string(parts[0]), string(parts[1]), string(parts[2])
	if phase != "spake2" && phase != "confirm" {
		dropPacket(src, "Dropped MSG with unknown phase %q from %s in room %s", phase, src, room)
		return
	}
	if !validRoomName(room) {
		dropPacket(src, "Dropped MSG with invalid room %q from %s", room, src)
		return
	}
	if !isValidHex(body) || len(body) > maxMSGBodyLen {
		dropPacket(src, "Dropped MSG with invalid body from %s in room %s", src, room)
		return
	}
	srcStr := src.String()

	shard := shardFor(room)
	shard.mu.RLock()
	entry, ok := shard.rooms[room]
	shard.mu.RUnlock()
	if !ok {
		dropPacket(src, "Dropped MSG for unknown room %s from %s", room, src)
		return
	}

	entry.mu.Lock()
	info, known := entry.udpClients[srcStr]
	if !known {
		entry.mu.Unlock()
		dropPacket(src, "Dropped message from unregistered client %s in room %s", srcStr, room)
		return
	}

	if isMsgRateLimited(info) {
		entry.mu.Unlock()
		dropPacket(src, "Rate-limited MSG from %s in room %s", srcStr, room)
		return
	}

	var targets []*net.UDPAddr
	for addr, ci := range entry.udpClients {
		if addr != srcStr && ci.resolvedAddr != nil {
			targets = append(targets, ci.resolvedAddr)
		}
	}
	entry.mu.Unlock()

	msgWire := []byte(fmt.Sprintf("MSGD %s %s\n", phase, body))
	for _, t := range targets {
		_ = writeRelay(conn, msgWire, t)
	}
	statMsgs.Add(1)
}
