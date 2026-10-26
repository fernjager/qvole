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
)

// relayServer holds the per-listener state that must not be shared between
// RunRelay instances. Previously writePool/writeMu were package globals, so a
// second RunRelay (tests, dual listeners, embedding) silently redirected the
// first instance's writes into the second instance's pool.
type relayServer struct {
	conn *net.UDPConn

	// writePool is a bounded semaphore that limits concurrent outbound
	// writes. When non-nil (production), writes are dispatched to
	// background goroutines so slow targets cannot pin relay workers.
	// When nil (tests), writes execute directly on the calling goroutine.
	writePool chan struct{}

	// writeMu serializes SetWriteDeadline + WriteTo pairs on the shared
	// conn, eliminating the race between concurrent workers.
	writeMu sync.Mutex
}

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

	r := &relayServer{conn: conn, writePool: make(chan struct{}, writePoolSize)}

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	pktCh := make(chan pkt, pktChanBuf)
	for i := 0; i < relayWorkers; i++ {
		go func() {
			for p := range pktCh {
				r.handlePacket(p.data, p.src)
				// handlePacket does not retain p.data past its return, so
				// the buffer goes back to the pool for the read loop.
				readPool.Put(p.data[:readBufSize])
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

// HandlePacket dispatches a raw inbound datagram to the appropriate REG/MSG
// handler using direct (synchronous) writes. RunRelay uses its own relayServer
// instance, so multiple relay instances never share pool/mutex state.
func HandlePacket(conn *net.UDPConn, data []byte, src *net.UDPAddr) {
	(&relayServer{conn: conn}).handlePacket(data, src)
}

func (r *relayServer) handlePacket(data []byte, src *net.UDPAddr) {
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
		r.handleReg(room, cookie, src)
	case len(trimmed) >= cmdPrefixLen && bytes.HasPrefix(trimmed, msgPrefix):
		payload := trimmed[cmdPrefixLen:]
		r.handleMsg(payload, src)
	default:
		end := len(trimmed)
		if end > unknownTypeMaxLogLen {
			end = unknownTypeMaxLogLen
		}
		dropPacket(src, "Dropped unknown packet type from %s: %q", src, string(trimmed[:end]))
	}
}

// writeRelay sends msg to addr via the relay's conn. In production
// (writePool != nil), writes are dispatched to a bounded goroutine pool with a
// per-conn mutex serialising SetWriteDeadline + WriteTo. This prevents slow
// targets from blocking relay workers (worker starvation) and eliminates the
// data race between concurrent SetWriteDeadline calls (undefined behaviour).
func (r *relayServer) writeRelay(msg []byte, addr *net.UDPAddr) error {
	if r.writePool != nil {
		select {
		case r.writePool <- struct{}{}:
		default:
			statDrops.Add(1)
			return nil
		}
		go func() {
			defer func() { <-r.writePool }()
			r.writeMu.Lock()
			r.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
			_, err := r.conn.WriteTo(msg, addr)
			r.writeMu.Unlock()
			if err != nil && dropLogLimiter.allow(ipKey(addr.String()), dropLogInterval) {
				util.LogRelay.PrintfWarn("UDP write to %s failed: %v", addr, err)
			}
		}()
		return nil
	}
	// Direct write (tests; writePool is nil).
	r.writeMu.Lock()
	r.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := r.conn.WriteTo(msg, addr)
	r.writeMu.Unlock()
	return err
}

const (
	pendingRegTTL      = 5 * time.Second
	pendingCookieBytes = 16
)

func (r *relayServer) sendREGD(room string, src *net.UDPAddr) {
	if err := r.writeRelay([]byte(fmt.Sprintf("REGD %s OK %s\n", room, src.String())), src); err != nil {
		util.LogRelay.PrintfWarn("REGD write to %s failed: %v", src, err)
	}
}

func (r *relayServer) sendRegChallenge(room string, cookie []byte, src *net.UDPAddr) {
	if err := r.writeRelay([]byte(fmt.Sprintf("REGD %s %x\n", room, cookie)), src); err != nil {
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

func (r *relayServer) handleReg(room string, cookie []byte, src *net.UDPAddr) {
	srcStr := src.String()
	srcIP := ipKey(srcStr)

	// Rate-limit every REG shape, including cookie-bearing completion and
	// re-registration. Previously only the no-cookie/rechallenge paths were
	// limited, so a random-cookie flood reached the pending/room locks and
	// burned CPU without ever being admitted.
	if isRegRateLimited(srcIP) {
		dropPacket(src, "Rate-limited REG from %s", srcStr)
		return
	}

	// If a cookie is present, this is the follow-up REG; verify it
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

		// Check the global pending store first; these are pre-cookie
		// registrations for rooms that don't exist yet.
		if pend := getPendingReg(room, srcStr); pend != nil {
			if !bytes.Equal(pend.cookie, cookieBytes) {
				dropPacket(src, "Dropped REG with mismatched cookie from %s in room %s (pending store)", srcStr, room)
				return
			}
			if time.Since(pend.createdAt) > pendingRegTTL {
				deletePendingReg(room, srcStr)
				dropPacket(src, "Dropped expired cookie from %s in room %s (pending store)", srcStr, room)
				return
			}
			// Cookie matches. Remove from pending store and admit the client,
			// creating the room if necessary.
			deletePendingReg(room, srcStr)

			shard := shardFor(room)
			// Stale rooms may live in any shard. Sweep globally *before*
			// taking the target shard lock: acquiring other shard locks while
			// holding this one can deadlock against another cookie completion
			// doing the same sweep in the opposite order. No lock is held
			// here, so this is safe.
			if totalRooms() >= maxRooms {
				if n := evictStaleRoomsAll(); n > 0 {
					util.LogRelay.PrintfWarn("Evicted %d stale room(s)", n)
				}
			}
			shard.mu.Lock()
			entry, ok := shard.rooms[room]
			if !ok {
				// Room doesn't exist yet; create it, re-checking all caps.
				if totalRooms() >= maxRooms {
					// Re-sweep the target shard under its lock (covers rooms
					// that went stale since the global sweep).
					if n := evictStaleRooms(shard); n > 0 {
						util.LogRelay.PrintfWarn("Evicted %d stale room(s)", n)
					}
				}
				if totalRooms() >= maxRooms {
					shard.mu.Unlock()
					dropPacket(src, "Room %s rejected: max rooms reached (cookie completion)", room)
					return
				}
				if countIPRooms(srcIP) >= maxRoomsPerIP {
					shard.mu.Unlock()
					dropPacket(src, "Room limit per IP reached for %s (cookie completion)", srcStr)
					return
				}
				entry = &roomEntry{
					udpClients: map[string]*udpClient{
						srcStr: {
							lastSeen:     time.Now(),
							rateLimiter:  rateLimiter{windowStart: time.Now()},
							resolvedAddr: pend.resolvedAddr,
						},
					},
					t: time.Now(),
				}
				shard.rooms[room] = entry
				totalRoomCount.Add(1)
				addIPRoom(srcIP, room)
				statRegs.Add(1)
				shard.mu.Unlock()
				util.LogRelay.Printf("Client %s joined room %s (created)", srcStr, room)
				r.sendREGD(room, src)
				return
			}
			// Room already exists (created by another pending that completed
			// first). Check caps before admitting. Lock the entry before
			// releasing the shard lock (shard → entry order) so
			// evictStaleRooms cannot delete the entry in between and leave
			// us admitting a client into an orphaned room.
			entry.mu.Lock()
			shard.mu.Unlock()
			// Evict stale clients (and expired in-room pending entries) before
			// the cap checks: an idle room's clients must not block a valid
			// cookie completion until the next cleanup tick.
			entry.sweep(room, time.Now())
			ipAlreadyInRoom := false
			for addr := range entry.udpClients {
				if ipKey(addr) == srcIP {
					ipAlreadyInRoom = true
					break
				}
			}
			if len(entry.udpClients) >= maxClientsHard && entry.udpClients[srcStr] == nil {
				entry.mu.Unlock()
				dropPacket(src, "Room %s at hard cap (%d), rejecting %s (cookie completion)", room, maxClientsHard, srcStr)
				return
			}
			if entry.udpClients[srcStr] == nil && entry.countPrefixClients(srcIP) >= maxClientsPerPrefixPerRoom {
				entry.mu.Unlock()
				dropPacket(src, "Room %s per-prefix client limit (%d) reached for %s (cookie completion)", room, maxClientsPerPrefixPerRoom, srcStr)
				return
			}
			if !ipAlreadyInRoom && countIPRooms(srcIP) >= maxRoomsPerIP {
				entry.mu.Unlock()
				dropPacket(src, "Room limit per IP reached for %s (cookie completion)", srcStr)
				return
			}
			u := &udpClient{
				lastSeen:     time.Now(),
				rateLimiter:  rateLimiter{windowStart: time.Now()},
				resolvedAddr: pend.resolvedAddr,
			}
			entry.udpClients[srcStr] = u
			entry.t = time.Now()
			if !ipAlreadyInRoom {
				addIPRoom(srcIP, room)
			}
			statRegs.Add(1)
			entry.mu.Unlock()
			util.LogRelay.Printf("Client %s joined room %s (cookie completion)", srcStr, room)
			r.sendREGD(room, src)
			return
		}

		// Not in pending store; check existing rooms. Hold the shard lock
		// until entry.mu is acquired (shard → entry order, same as the
		// no-cookie path below) so evictStaleRooms cannot delete the entry
		// in between and orphan the admission.
		shard := shardFor(room)
		shard.mu.RLock()
		entry, ok := shard.rooms[room]
		if !ok {
			shard.mu.RUnlock()
			// The room doesn't exist: the pending registration for this cookie
			// expired (pendingRegTTL) or was evicted. Re-issue a fresh
			// challenge via the pending store instead of dropping, so a client
			// whose handshake outlived the pending entry can still complete.
			// No room is created here, so this can't be abused to inflate the
			// room table; the same caps as an initial REG apply.
			if countIPRooms(srcIP) >= maxRoomsPerIP {
				dropPacket(src, "Room limit per IP reached for %s", srcStr)
				return
			}
			if countPendingForIP(srcIP) >= maxPendingPerIP {
				dropPacket(src, "Pending limit per IP reached for %s", srcStr)
				return
			}
			cb := make([]byte, pendingCookieBytes)
			if _, err := rand.Read(cb); err != nil {
				util.LogRelay.PrintfWarn("rand cookie failed: %v", err)
				return
			}
			if !storePendingReg(room, srcStr, &pendingReg{
				cookie:       cb,
				createdAt:    time.Now(),
				resolvedAddr: src,
			}) {
				dropPacket(src, "Pending store full for %s", srcStr)
				return
			}
			r.sendRegChallenge(room, cb, src)
			return
		}
		entry.mu.Lock()
		shard.mu.RUnlock()
		pend, exists := entry.pending[srcStr]
		if !exists || !bytes.Equal(pend.cookie, cookieBytes) {
			entry.mu.Unlock()
			dropPacket(src, "Dropped REG with mismatched cookie from %s in room %s", srcStr, room)
			return
		}
		if time.Since(pend.createdAt) > pendingRegTTL {
			entry.removePending(srcStr)
			entry.mu.Unlock()
			dropPacket(src, "Dropped expired cookie from %s in room %s", srcStr, room)
			return
		}
		// Cookie matches; admit the client, re-checking caps. Evict stale
		// clients first so an idle room's clients do not count against the
		// hard cap (pend was captured above, so this cannot invalidate the
		// completion).
		entry.sweep(room, time.Now())
		if len(entry.udpClients) >= maxClientsHard && entry.udpClients[srcStr] == nil {
			entry.mu.Unlock()
			dropPacket(src, "Room %s at hard cap (%d), rejecting %s (cookie admission)", room, maxClientsHard, srcStr)
			return
		}
		if entry.udpClients[srcStr] == nil {
			ipAlreadyInRoom := false
			for addr := range entry.udpClients {
				if ipKey(addr) == srcIP {
					ipAlreadyInRoom = true
					break
				}
			}
			if !ipAlreadyInRoom && countIPRooms(srcIP) >= maxRoomsPerIP {
				entry.mu.Unlock()
				dropPacket(src, "Room limit per IP reached for %s (cookie admission)", srcStr)
				return
			}
		}
		entry.removePending(srcStr)
		u := &udpClient{
			lastSeen:     time.Now(),
			rateLimiter:  rateLimiter{windowStart: time.Now()},
			resolvedAddr: pend.resolvedAddr,
		}
		wasReReg := entry.udpClients[srcStr] != nil
		entry.udpClients[srcStr] = u
		entry.t = time.Now()

		if !wasReReg {
			ipAlreadyInRoom := false
			for addr := range entry.udpClients {
				if addr != srcStr && ipKey(addr) == srcIP {
					ipAlreadyInRoom = true
					break
				}
			}
			if !ipAlreadyInRoom {
				addIPRoom(srcIP, room)
			}
		}
		statRegs.Add(1)
		entry.mu.Unlock()
		util.LogRelay.Printf("Client %s joined room %s", srcStr, room)

		r.sendREGD(room, src)
		return
	}

	// No cookie: initial REG.
	shard := shardFor(room)

	shard.mu.Lock()
	entry, ok := shard.rooms[room]
	if !ok {
		// Room does not exist. Store a pending registration instead of
		// creating the room immediately. This prevents room-table exhaustion
		// from spoofed source IPs.
		shard.mu.Unlock()

		// Apply the per-prefix room cap only when creating a new room. An
		// already-admitted client must still be able to re-REG a room it is
		// already in, even if the prefix is at the cap.
		if countIPRooms(srcIP) >= maxRoomsPerIP {
			dropPacket(src, "Room limit per IP reached for %s", srcStr)
			return
		}

		if countPendingForIP(srcIP) >= maxPendingPerIP {
			dropPacket(src, "Pending limit per IP reached for %s", srcStr)
			return
		}

		cb := make([]byte, pendingCookieBytes)
		if _, err := rand.Read(cb); err != nil {
			util.LogRelay.PrintfWarn("rand cookie failed: %v", err)
			return
		}
		if !storePendingReg(room, srcStr, &pendingReg{
			cookie:       cb,
			createdAt:    time.Now(),
			resolvedAddr: src,
		}) {
			dropPacket(src, "Pending store full for %s", srcStr)
			return
		}
		r.sendRegChallenge(room, cb, src)
		return
	}
	entry.mu.Lock()
	shard.mu.Unlock()

	entry.sweep(room, time.Now())

	// Check hard cap. This counts admitted udpClients only; in-room pending
	// registrations are bounded separately by the per-IP pending cap below.
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
		r.sendREGD(room, src)
		return
	}

	if entry.countPrefixClients(srcIP) >= maxClientsPerPrefixPerRoom {
		entry.mu.Unlock()
		dropPacket(src, "Room %s per-prefix client limit (%d) reached for %s", room, maxClientsPerPrefixPerRoom, srcStr)
		return
	}

	// Check for existing pending registration; re-issue the same cookie.
	if pend, exists := entry.pending[srcStr]; exists {
		pend.createdAt = time.Now()
		pend.resolvedAddr = src
		cb := pend.cookie
		entry.mu.Unlock()
		r.sendRegChallenge(room, cb, src)
		return
	}

	// Fresh pending registration. Enforce the per-IP pending cap like the
	// global-store paths do, counting this room's pending map as well, which
	// countPendingForIP does not see, so a REG flood with randomized source
	// ports against one existing room cannot grow entry.pending unboundedly.
	if countPendingForIP(srcIP)+entry.pendingCountForIP(srcIP) >= maxPendingPerIP {
		entry.mu.Unlock()
		dropPacket(src, "Pending limit per IP reached for %s", srcStr)
		return
	}
	cb := make([]byte, pendingCookieBytes)
	if _, err := rand.Read(cb); err != nil {
		entry.mu.Unlock()
		util.LogRelay.PrintfWarn("rand cookie failed: %v", err)
		return
	}
	if !entry.addPending(srcStr, srcIP, &pendingReg{cookie: cb, createdAt: time.Now(), resolvedAddr: src}) {
		entry.mu.Unlock()
		dropPacket(src, "Pending store full for %s in room %s", srcStr, room)
		return
	}
	entry.mu.Unlock()
	r.sendRegChallenge(room, cb, src)
}

func (r *relayServer) handleMsg(payload []byte, src *net.UDPAddr) {
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
		_ = r.writeRelay(msgWire, t)
	}
	// Count only messages actually relayed to at least one peer; a MSG from
	// the only client in a room writes nothing.
	if len(targets) > 0 {
		statMsgs.Add(1)
	}
}
