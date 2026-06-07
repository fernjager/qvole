package relay

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"time"

	"github.com/fernjager/qvole/internal/util"
)

const dropLogInterval = time.Second

const (
	readBufSize          = 1500
	cmdPrefixLen         = 4
	unknownTypeMaxLogLen = 40

	defaultPktChanBuf         = 256
	defaultRelayStatsInterval = 5 * time.Minute
	defaultWriteDeadline      = 500 * time.Millisecond
)

var (
	pktChanBuf         int           = defaultPktChanBuf
	relayStatsInterval time.Duration = defaultRelayStatsInterval
	writeDeadline      time.Duration = defaultWriteDeadline
)

func init() {
	pktChanBuf = util.EnvInt("QVOLE_RELAY_PKT_CHAN_BUF", defaultPktChanBuf)
	relayStatsInterval = util.EnvDuration("QVOLE_RELAY_STATS_INTERVAL_MS", defaultRelayStatsInterval)
	writeDeadline = util.EnvDuration("QVOLE_RELAY_WRITE_DEADLINE_MS", defaultWriteDeadline)
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

	util.LogRelay.PrintfSuccess("Listening on UDP %s (%d workers)", util.Bold(addr), relayWorkers)

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
		data := make([]byte, n)
		copy(data, buf[:n])
		select {
		case pktCh <- pkt{data: data, src: src}:
		default:
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
		room := string(bytes.TrimSpace(trimmed[cmdPrefixLen:]))
		if len(room) == 0 {
			dropPacket(src, "Dropped REG with empty room from %s", src)
			return
		}
		if !validRoomName(room) {
			dropPacket(src, "Dropped REG with invalid room %q from %s", room, src)
			return
		}
		handleReg(conn, room, src)
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

func writeRelay(conn *net.UDPConn, msg []byte, addr *net.UDPAddr) error {
	conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	_, err := conn.WriteTo(msg, addr)
	return err
}

func sendREGD(conn *net.UDPConn, room string, src *net.UDPAddr) {
	if err := writeRelay(conn, []byte(fmt.Sprintf("REGD %s %s\n", room, src.String())), src); err != nil {
		util.LogRelay.PrintfWarn("REGD write to %s failed: %v", src, err)
	}
}

func dropPacket(src *net.UDPAddr, format string, args ...any) {
	if !dropLogLimiter.allow(ipKey(src.String()), dropLogInterval) {
		return
	}
	statDrops.Add(1)
	util.LogRelay.PrintfWarn(format, args...)
}

// --- UDP handlers ---

func handleReg(conn *net.UDPConn, room string, src *net.UDPAddr) {
	srcStr := src.String()
	if isRegRateLimited(ipKey(srcStr)) {
		util.LogRelay.PrintfWarn("Rate-limited REG from %s", srcStr)
		return
	}

	if countIPRooms(ipKey(srcStr)) >= maxRoomsPerIP {
		util.LogRelay.PrintfWarn("Room limit per IP reached for %s", srcStr)
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
			util.LogRelay.PrintfWarn("Room %s rejected: max rooms reached", room)
			return
		}
		entry = &roomEntry{
			udpClients: map[string]*udpClient{
				srcStr: {lastSeen: time.Now(), rateLimiter: rateLimiter{windowStart: time.Now()}, resolvedAddr: src},
			},
			t: time.Now(),
		}
		shard.rooms[room] = entry
		totalRoomCount.Add(1)
		addIPRoom(ipKey(srcStr), room)
		shard.mu.Unlock()
		statRegs.Add(1)
		sendREGD(conn, room, src)
		return
	}
	entry.mu.Lock()
	shard.mu.Unlock()

	if len(entry.udpClients) >= maxClientsPerRoom && entry.udpClients[srcStr] == nil {
		entry.mu.Unlock()
		util.LogRelay.PrintfWarn("Room %s full, rejecting %s", room, srcStr)
		return
	}

	if entry.udpClients[srcStr] != nil {
		entry.udpClients[srcStr].lastSeen = time.Now()
		entry.udpClients[srcStr].resolvedAddr = src
		entry.t = time.Now()
		entry.mu.Unlock()
		statRegs.Add(1)
		sendREGD(conn, room, src)
		return
	}

	myIP := ipKey(srcStr)
	ipAlreadyInRoom := false
	for addr := range entry.udpClients {
		if ipKey(addr) == myIP {
			ipAlreadyInRoom = true
			break
		}
	}
	entry.udpClients[srcStr] = &udpClient{
		lastSeen:     time.Now(),
		rateLimiter:  rateLimiter{windowStart: time.Now()},
		resolvedAddr: src,
	}
	if !ipAlreadyInRoom {
		addIPRoom(myIP, room)
	}
	entry.t = time.Now()
	statRegs.Add(1)
	util.LogRelay.Printf("Client %s joined room %s", srcStr, room)
	entry.mu.Unlock()

	sendREGD(conn, room, src)
}

func handleMsg(conn *net.UDPConn, payload []byte, src *net.UDPAddr) {
	parts := bytes.SplitN(payload, []byte(" "), 3)
	if len(parts) != 3 {
		dropPacket(src, "Dropped malformed MSG from %s", src)
		return
	}
	room, phase, body := string(parts[0]), string(parts[1]), string(parts[2])
	if !validRoomName(room) {
		dropPacket(src, "Dropped MSG with invalid room %q from %s", room, src)
		return
	}
	if !isValidHex(body) {
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
		util.LogRelay.PrintfWarn("Dropped message from unregistered client %s in room %s", srcStr, room)
		return
	}

	if isMsgRateLimited(info) {
		entry.mu.Unlock()
		util.LogRelay.PrintfWarn("Rate-limited MSG from %s in room %s", srcStr, room)
		return
	}

	var targetsUDP []*udpClient
	for addr, ci := range entry.udpClients {
		if addr != srcStr && ci.resolvedAddr != nil {
			targetsUDP = append(targetsUDP, ci)
		}
	}
	entry.mu.Unlock()

	msgWire := []byte(fmt.Sprintf("MSGD %s %s\n", phase, body))
	for _, ci := range targetsUDP {
		if err := writeRelay(conn, msgWire, ci.resolvedAddr); err != nil {
			util.LogRelay.PrintfWarn("UDP write to %s failed: %v", ci.resolvedAddr, err)
		}
	}
	statMsgs.Add(1)
}
