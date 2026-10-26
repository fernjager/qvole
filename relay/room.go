package relay

import (
	"context"
	"hash/fnv"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fernjager/qvole/internal/util"
)

const (
	numShards      = 16
	numRegShards   = 16
	maxRoomNameLen = 64
	maxDatagramLen = 1400

	defaultMaxRooms           = 10000
	defaultMaxRoomsPerIP      = 10
	defaultMaxMsgRate         = 10
	defaultRegRate            = 10
	defaultRateWindow         = 1 * time.Second
	defaultRegCleanupInterval = 1 * time.Minute
	defaultRegTTL             = 1 * time.Minute
	defaultRelayWorkers       = 4
	defaultMaxClientsHard     = 20
	// defaultMaxClientsPerPrefixPerRoom stops one source (or one IPv6 /64)
	// from filling a room's hard cap on its own. Four leaves headroom for
	// restarts/stale source ports while still preventing a single host from
	// occupying all 20 slots.
	defaultMaxClientsPerPrefixPerRoom = 4

	// maxPendingPerIP caps the number of pending (pre-cookie) registrations
	// allowed per source IP across all rooms. This prevents a single IP from
	// flooding the pending store with cookie challenges for non-existent rooms.
	defaultMaxPendingPerIP = 50

	// maxPendingGlobal caps the total number of pending registrations across
	// all IPs. The per-IP cap alone is the wrong axis for this store: a
	// botnet (or a single attacker over an IPv6 /64) can present many source
	// IPs. Bound the store as a whole so memory is proportional to the cap,
	// not to the flood rate times the TTL.
	defaultMaxPendingGlobal = 65536

	// In-room pending registrations have their own budget and a per-room cap.
	// Sharing maxPendingGlobal with the global store let a flood against one
	// existing room freeze new-room registration relay-wide.
	defaultMaxInRoomPendingGlobal = 65536
	defaultMaxPendingPerRoom      = 256

	// maxRegShardEntries is a hard cap on rate-limit map entries per
	// shard. Under a source-IP flood, entries accumulate fast; this cap
	// prevents unbounded memory growth.
	maxRegShardEntries     = 100_000
	rateMapCleanupInterval = 5 * time.Second
)

var (
	maxRooms                   int           = defaultMaxRooms
	maxRoomsPerIP              int           = defaultMaxRoomsPerIP
	maxMsgRate                 int           = defaultMaxMsgRate
	maxRegRate                 int           = defaultRegRate
	rateWindow                 time.Duration = defaultRateWindow
	regCleanupInterval         time.Duration = defaultRegCleanupInterval
	regTTL                     time.Duration = defaultRegTTL
	relayWorkers               int           = defaultRelayWorkers
	maxClientsHard             int           = defaultMaxClientsHard
	maxClientsPerPrefixPerRoom int           = defaultMaxClientsPerPrefixPerRoom
	maxPendingPerIP            int           = defaultMaxPendingPerIP
	maxPendingGlobal           int           = defaultMaxPendingGlobal
	maxInRoomPending           int           = defaultMaxInRoomPendingGlobal
	maxPendingPerRoom          int           = defaultMaxPendingPerRoom
)

type rateLimiter struct {
	msgCount    int
	windowStart time.Time
}

// isLimited checks whether the rate limiter has exceeded the configured message
// rate. It must be called with the entry mutex held to prevent data races
// on msgCount and windowStart.
func (r *rateLimiter) isLimited() bool {
	now := time.Now()
	if now.Sub(r.windowStart) > rateWindow {
		r.msgCount = 0
		r.windowStart = now
	}
	if r.msgCount >= maxMsgRate {
		return true
	}
	r.msgCount++
	return false
}

type udpClient struct {
	lastSeen time.Time
	rateLimiter
	resolvedAddr *net.UDPAddr
}

type roomEntry struct {
	mu          sync.Mutex
	udpClients  map[string]*udpClient
	pending     map[string]*pendingReg // cookie challenge, keyed by srcStr
	pendingByIP map[string]int         // per-IP count of pending, mirrors pending
	t           time.Time
	// nextPendingSweep throttles expiry scans of pending. Without it, every
	// REG would scan the whole map while holding entry.mu; an attacker who
	// fills the map (up to the global budget) could turn each REG into O(n)
	// work and stall the room.
	nextPendingSweep time.Time
}

// pendingReg holds a return-routability cookie for a client that sent REG
// but has not yet completed the cookie handshake. Prevents source-address
// spoofing from installing victim addresses into a room.
type pendingReg struct {
	cookie       []byte
	createdAt    time.Time
	resolvedAddr *net.UDPAddr
}

type roomShard struct {
	mu    sync.RWMutex
	rooms map[string]*roomEntry
}

type regLimitShard struct {
	mu    sync.Mutex
	sent  map[string]int
	times map[string]time.Time
}

type ipCountShard struct {
	mu    sync.Mutex
	rooms map[string]map[string]struct{}
}

// pendingIPShard holds a per-IP count of entries in the global pending store,
// so countPendingForIP is O(1) instead of scanning the whole store.
type pendingIPShard struct {
	mu     sync.Mutex
	counts map[string]int
}

// pendingStoreShard holds pre-cookie registrations for rooms that do not yet
// exist. Rooms are only created when the cookie handshake completes, preventing
// room-table exhaustion from spoofed initial REGs.
type pendingStoreShard struct {
	mu   sync.Mutex
	regs map[string]map[string]*pendingReg // room -> srcStr -> pendingReg
}

var (
	shards          [numShards]roomShard
	regShards       [numRegShards]regLimitShard
	totalRoomCount  atomic.Int64
	ipCounts        [numShards]ipCountShard
	pendingRegs     [numShards]pendingStoreShard
	pendingIPCounts [numShards]pendingIPShard
	pendingTotal    atomic.Int64
	// inRoomPendingTotal counts pending registrations held in per-room
	// roomEntry.pending maps. They have their own relay-wide budget and a
	// per-room cap, separate from the pre-cookie store used for rooms that do
	// not exist yet. A flood against one existing room cannot freeze new-room
	// registration, and memory stays bounded.
	inRoomPendingTotal atomic.Int64

	statRegs  atomic.Int64
	statMsgs  atomic.Int64
	statDrops atomic.Int64

	dropLogLimiter = new(ipRateLimiter)
)

// Bounds for the drop-log rate limiter. The per-IP interval alone is not
// enough: a spoofed-source or IPv6-prefix flood can present an unlimited
// number of distinct source IPs, each of which would otherwise allocate a map
// entry and emit one log line. A global lines-per-second budget plus a hard
// map cap keeps memory and log volume bounded.
var (
	maxDropLogEntries    = 100_000
	maxDropLogsPerSecond = 100
)

type ipRateLimiter struct {
	mu          sync.Mutex
	last        map[string]time.Time
	windowStart time.Time
	windowCount int
	// Optional per-instance overrides (used by tests); zero means use the
	// package defaults. Keeping them out of package globals avoids racing the
	// production limiter.
	maxEntries int
	maxPerSec  int
}

func (l *ipRateLimiter) allow(ip string, interval time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	perSec := l.maxPerSec
	if perSec <= 0 {
		perSec = maxDropLogsPerSecond
	}
	maxEntries := l.maxEntries
	if maxEntries <= 0 {
		maxEntries = maxDropLogEntries
	}
	now := time.Now()
	if now.Sub(l.windowStart) >= time.Second {
		l.windowStart = now
		l.windowCount = 0
	}
	if l.windowCount >= perSec {
		return false
	}
	if last, ok := l.last[ip]; ok && now.Sub(last) < interval {
		return false
	}
	if l.last == nil {
		l.last = make(map[string]time.Time)
	}
	if _, ok := l.last[ip]; !ok && len(l.last) >= maxEntries {
		return false
	}
	l.last[ip] = now
	l.windowCount++
	return true
}

func (l *ipRateLimiter) cleanupStale(maxAge time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for ip, last := range l.last {
		if now.Sub(last) > maxAge {
			delete(l.last, ip)
		}
	}
}

func init() {
	maxRooms = util.EnvInt("QVOLE_RELAY_MAX_ROOMS", defaultMaxRooms)
	maxRoomsPerIP = util.EnvInt("QVOLE_RELAY_MAX_ROOMS_PER_IP", defaultMaxRoomsPerIP)
	maxClientsHard = util.EnvInt("QVOLE_RELAY_MAX_CLIENTS_HARD", defaultMaxClientsHard)
	maxClientsPerPrefixPerRoom = util.EnvInt("QVOLE_RELAY_MAX_CLIENTS_PER_PREFIX_PER_ROOM", defaultMaxClientsPerPrefixPerRoom)
	maxMsgRate = util.EnvInt("QVOLE_RELAY_MSG_RATE", defaultMaxMsgRate)
	maxRegRate = util.EnvInt("QVOLE_RELAY_REG_RATE", defaultRegRate)
	maxPendingPerIP = util.EnvInt("QVOLE_RELAY_MAX_PENDING_PER_IP", defaultMaxPendingPerIP)
	maxPendingGlobal = util.EnvInt("QVOLE_RELAY_MAX_PENDING", defaultMaxPendingGlobal)
	maxInRoomPending = util.EnvInt("QVOLE_RELAY_MAX_INROOM_PENDING", defaultMaxInRoomPendingGlobal)
	maxPendingPerRoom = util.EnvInt("QVOLE_RELAY_MAX_PENDING_PER_ROOM", defaultMaxPendingPerRoom)
	rateWindow = util.EnvDuration("QVOLE_RELAY_RATE_WINDOW_MS", defaultRateWindow)
	regCleanupInterval = util.EnvDuration("QVOLE_RELAY_CLEANUP_INTERVAL_MS", defaultRegCleanupInterval)
	regTTL = util.EnvDuration("QVOLE_RELAY_TTL_MS", defaultRegTTL)
	relayWorkers = util.EnvInt("QVOLE_RELAY_WORKERS", defaultRelayWorkers)

	for i := range shards {
		shards[i].rooms = make(map[string]*roomEntry)
	}
	for i := range regShards {
		regShards[i].sent = make(map[string]int)
		regShards[i].times = make(map[string]time.Time)
	}
	for i := range ipCounts {
		ipCounts[i].rooms = make(map[string]map[string]struct{})
	}
	for i := range pendingRegs {
		pendingRegs[i].regs = make(map[string]map[string]*pendingReg)
	}
	for i := range pendingIPCounts {
		pendingIPCounts[i].counts = make(map[string]int)
	}
}

func shardIdx(key string, n uint32) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32() % n
}

func shardFor(room string) *roomShard         { return &shards[shardIdx(room, numShards)] }
func regShardFor(src string) *regLimitShard   { return &regShards[shardIdx(src, numRegShards)] }
func ipCountShardFor(ip string) *ipCountShard { return &ipCounts[shardIdx(ip, numShards)] }
func pendingIPShardFor(ip string) *pendingIPShard {
	return &pendingIPCounts[shardIdx(ip, numShards)]
}
func pendingShardFor(room string) *pendingStoreShard {
	return &pendingRegs[shardIdx(room, numShards)]
}

func ipKey(src string) string {
	host, _, err := net.SplitHostPort(src)
	if err != nil {
		return src
	}
	// Canonicalize the address so textual variants of the same IP
	// (e.g., "2001:db8::1" vs "2001:db8:0:0:0:0:0:1" vs "::ffff:1.2.3.4"
	// vs "1.2.3.4") collapse to a single key. For IPv6, normalize to the /64
	// prefix: a single host normally controls an entire /64 and could
	// otherwise rotate addresses to bypass every per-IP cap. Loopback stays
	// exact because local tests use 127.0.0.1 and ::1 directly.
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil || ip.IsLoopback() {
			return ip.String()
		}
		return ip.Mask(net.CIDRMask(64, 128)).String()
	}
	return host
}

func addIPRoom(ip, room string) {
	sh := ipCountShardFor(ip)
	sh.mu.Lock()
	set, ok := sh.rooms[ip]
	if !ok {
		set = make(map[string]struct{})
		sh.rooms[ip] = set
	}
	set[room] = struct{}{}
	sh.mu.Unlock()
}

func removeIPRoom(ip, room string) {
	sh := ipCountShardFor(ip)
	sh.mu.Lock()
	set, ok := sh.rooms[ip]
	if !ok {
		sh.mu.Unlock()
		return
	}
	delete(set, room)
	if len(set) == 0 {
		delete(sh.rooms, ip)
	}
	sh.mu.Unlock()
}

func totalRooms() int {
	return int(totalRoomCount.Load())
}

func totalClients() int {
	var n int
	for i := range shards {
		sh := &shards[i]
		sh.mu.RLock()
		for _, entry := range sh.rooms {
			entry.mu.Lock()
			n += len(entry.udpClients)
			entry.mu.Unlock()
		}
		sh.mu.RUnlock()
	}
	return n
}

func validRoomName(room string) bool {
	if len(room) == 0 || len(room) > maxRoomNameLen {
		return false
	}
	for _, c := range room {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func isValidHex(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func isRegRateLimited(src string) bool {
	sh := regShardFor(src)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := time.Now()
	reset, ok := sh.times[src]
	if !ok {
		if len(sh.times) >= maxRegShardEntries {
			// Map is at the hard cap (flood). Rate-limit new entries.
			return true
		}
		sh.sent[src] = 1
		sh.times[src] = now
		return false
	}
	if now.Sub(reset) > rateWindow {
		sh.sent[src] = 1
		sh.times[src] = now
		return false
	}
	if sh.sent[src] >= maxRegRate {
		return true
	}
	sh.sent[src]++
	return false
}

func isMsgRateLimited(client *udpClient) bool {
	return client.isLimited()
}

func resetRegRateLimits() {
	for i := range regShards {
		sh := &regShards[i]
		sh.mu.Lock()
		clear(sh.sent)
		clear(sh.times)
		sh.mu.Unlock()
	}
}

func resetIPCounts() {
	for i := range ipCounts {
		sh := &ipCounts[i]
		sh.mu.Lock()
		sh.rooms = make(map[string]map[string]struct{})
		sh.mu.Unlock()
	}
}

func resetRooms() {
	for i := range shards {
		sh := &shards[i]
		sh.mu.Lock()
		sh.rooms = make(map[string]*roomEntry)
		sh.mu.Unlock()
	}
	totalRoomCount.Store(0)
	resetPendingRegs()
	// All room entries were dropped above, so their pending entries are gone
	// with them.
	inRoomPendingTotal.Store(0)
}

func countIPRooms(ip string) int {
	sh := ipCountShardFor(ip)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return len(sh.rooms[ip])
}

// --- Pending registration store (pre-cookie, no room created yet) ---

// incPendingIP / decPendingIP maintain the per-IP and global pending counts.
// Every mutation of pendingRegs must be mirrored through these (or
// addPendingIP for a batch) so countPendingForIP and the global cap stay
// accurate.
func addPendingIP(ip string, delta int) {
	sh := pendingIPShardFor(ip)
	sh.mu.Lock()
	if n := sh.counts[ip] + delta; n > 0 {
		sh.counts[ip] = n
	} else {
		delete(sh.counts, ip)
	}
	sh.mu.Unlock()
	pendingTotal.Add(int64(delta))
}

// globalPendingBudgetReached reports whether the pre-cookie store for
// non-existent rooms is full. It deliberately excludes in-room pending: a
// flood against one existing room must not prevent new rooms from being
// registered.
func globalPendingBudgetReached() bool {
	return pendingTotal.Load() >= int64(maxPendingGlobal)
}

// inRoomPendingBudgetReached reports whether the separate relay-wide budget for
// pending entries in existing rooms is full.
func inRoomPendingBudgetReached() bool {
	return inRoomPendingTotal.Load() >= int64(maxInRoomPending)
}

// adjustInRoomPending atomically adds delta to inRoomPendingTotal, clamping at
// zero so test resets (which clear the maps without walking every room) cannot
// drive the in-room budget negative.
func adjustInRoomPending(delta int64) {
	for {
		old := inRoomPendingTotal.Load()
		n := old + delta
		if n < 0 {
			n = 0
		}
		if inRoomPendingTotal.CompareAndSwap(old, n) {
			return
		}
	}
}

// storePendingReg inserts p, returning false when the global pending cap has
// been reached. Re-storing an existing (room, src) key replaces it without
// changing the counts.
func storePendingReg(room, srcStr string, p *pendingReg) bool {
	sh := pendingShardFor(room)
	sh.mu.Lock()
	rm := sh.regs[room]
	var existed bool
	if rm != nil {
		_, existed = rm[srcStr]
	}
	// Check the budget before allocating an inner map: after the cap is hit,
	// an attacker can otherwise send REGs with fresh room names and make every
	// rejected attempt allocate an empty map that survives until cleanup.
	if !existed && globalPendingBudgetReached() {
		if rm != nil && len(rm) == 0 {
			delete(sh.regs, room)
		}
		sh.mu.Unlock()
		return false
	}
	if rm == nil {
		rm = make(map[string]*pendingReg)
		sh.regs[room] = rm
	}
	rm[srcStr] = p
	sh.mu.Unlock()
	if !existed {
		addPendingIP(ipKey(srcStr), 1)
	}
	return true
}

func getPendingReg(room, srcStr string) *pendingReg {
	sh := pendingShardFor(room)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	rm, ok := sh.regs[room]
	if !ok {
		return nil
	}
	return rm[srcStr]
}

func deletePendingReg(room, srcStr string) {
	sh := pendingShardFor(room)
	sh.mu.Lock()
	rm, ok := sh.regs[room]
	if !ok {
		sh.mu.Unlock()
		return
	}
	_, existed := rm[srcStr]
	delete(rm, srcStr)
	if len(rm) == 0 {
		delete(sh.regs, room)
	}
	sh.mu.Unlock()
	if existed {
		addPendingIP(ipKey(srcStr), -1)
	}
}

// countPendingForIP returns the number of pending registrations across all
// rooms for the given IP. O(1): reads the per-IP counter shard.
func countPendingForIP(ip string) int {
	sh := pendingIPShardFor(ip)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.counts[ip]
}

// removeStalePendingRegs evicts pending registrations older than pendingRegTTL
// from the global pending store. Called during periodic cleanup.
func removeStalePendingRegs(now time.Time) {
	for i := range pendingRegs {
		sh := &pendingRegs[i]
		removed := make(map[string]int)
		sh.mu.Lock()
		for room, rm := range sh.regs {
			for srcStr, p := range rm {
				if now.Sub(p.createdAt) > pendingRegTTL {
					delete(rm, srcStr)
					removed[ipKey(srcStr)]++
				}
			}
			if len(rm) == 0 {
				delete(sh.regs, room)
			}
		}
		sh.mu.Unlock()
		for ip, n := range removed {
			addPendingIP(ip, -n)
		}
	}
}

func resetPendingRegs() {
	for i := range pendingRegs {
		sh := &pendingRegs[i]
		sh.mu.Lock()
		sh.regs = make(map[string]map[string]*pendingReg)
		sh.mu.Unlock()
	}
	for i := range pendingIPCounts {
		sh := &pendingIPCounts[i]
		sh.mu.Lock()
		sh.counts = make(map[string]int)
		sh.mu.Unlock()
	}
	pendingTotal.Store(0)
	inRoomPendingTotal.Store(0)
}

// addPending records an in-room pending registration and its per-IP count.
// Must be called with entry.mu held. It returns false when the per-room cap or
// the relay-wide in-room budget is exhausted, in which case the caller must
// reject the REG instead of growing the map without bound.
func (entry *roomEntry) addPending(srcStr, ip string, p *pendingReg) bool {
	if _, exists := entry.pending[srcStr]; !exists {
		if len(entry.pending) >= maxPendingPerRoom || inRoomPendingBudgetReached() {
			return false
		}
		if entry.pending == nil {
			entry.pending = make(map[string]*pendingReg)
		}
		if entry.pendingByIP == nil {
			entry.pendingByIP = make(map[string]int)
		}
		entry.pendingByIP[ip]++
		adjustInRoomPending(1)
	}
	entry.pending[srcStr] = p
	return true
}

// removePending deletes an in-room pending registration and its per-IP count.
// Must be called with entry.mu held.
func (entry *roomEntry) removePending(srcStr string) {
	if _, exists := entry.pending[srcStr]; !exists {
		return
	}
	delete(entry.pending, srcStr)
	adjustInRoomPending(-1)
	if entry.pendingByIP != nil {
		ip := ipKey(srcStr)
		if n := entry.pendingByIP[ip]; n <= 1 {
			delete(entry.pendingByIP, ip)
		} else {
			entry.pendingByIP[ip] = n - 1
		}
	}
}

// removeAllPending drops every in-room pending registration and returns their
// budget. It must be called with entry.mu held when the room is being deleted,
// so pending entries that are still within their TTL do not leak the shared
// budget.
func (entry *roomEntry) removeAllPending() {
	n := len(entry.pending)
	entry.pending = nil
	entry.pendingByIP = nil
	if n > 0 {
		adjustInRoomPending(-int64(n))
	}
}

// pendingCountForIP returns the number of pending registrations for the given
// IP within this room entry. Must be called with entry.mu held.
func (entry *roomEntry) pendingCountForIP(ip string) int {
	return entry.pendingByIP[ip]
}

// countPrefixClients returns how many admitted clients in this room belong to
// the same source prefix (an exact IPv4 address or an IPv6 /64, per ipKey).
// Must be called with entry.mu held.
func (entry *roomEntry) countPrefixClients(prefix string) int {
	n := 0
	for addr := range entry.udpClients {
		if ipKey(addr) == prefix {
			n++
		}
	}
	return n
}

func (entry *roomEntry) removeStaleClients(room string, now time.Time) {
	for addr, info := range entry.udpClients {
		if now.Sub(info.lastSeen) > regTTL {
			ip := ipKey(addr)
			stillInRoom := false
			for other := range entry.udpClients {
				if other != addr && ipKey(other) == ip {
					stillInRoom = true
					break
				}
			}
			if !stillInRoom {
				removeIPRoom(ip, room)
			}
			delete(entry.udpClients, addr)
		}
	}
}

// removeStalePending evicts expired in-room pending registrations, keeping the
// per-IP counts and the in-room pending budget in sync. It is O(len(pending)),
// so callers on the hot REG path should use sweep (throttled) instead.
func (entry *roomEntry) removeStalePending(now time.Time) {
	for addr, pend := range entry.pending {
		if now.Sub(pend.createdAt) > pendingRegTTL {
			entry.removePending(addr)
		}
	}
	if len(entry.pending) == 0 {
		entry.pending = nil
	}
	if len(entry.pendingByIP) == 0 {
		entry.pendingByIP = nil
	}
	entry.nextPendingSweep = now.Add(pendingRegTTL)
}

// sweep performs the per-REG stale bookkeeping. Client expiry is cheap (the
// room is capped at maxClientsHard); pending expiry is throttled to once per
// pendingRegTTL so a large pending map cannot make every REG O(n).
func (entry *roomEntry) sweep(room string, now time.Time) {
	entry.removeStaleClients(room, now)
	if !now.Before(entry.nextPendingSweep) {
		entry.removeStalePending(now)
	}
}

func evictStaleRooms(shard *roomShard) int {
	now := time.Now()
	evicted := 0
	for room, entry := range shard.rooms {
		entry.mu.Lock()
		entry.removeStaleClients(room, now)
		entry.removeStalePending(now)
		empty := len(entry.udpClients) == 0
		stale := now.Sub(entry.t) > regTTL
		if empty && stale {
			// Drop any still-fresh pending entries too, otherwise their
			// shared-budget accounting would leak when the room disappears.
			entry.removeAllPending()
		}
		entry.mu.Unlock()
		if empty && stale {
			delete(shard.rooms, room)
			totalRoomCount.Add(-1)
			evicted++
		}
	}
	return evicted
}

// evictStaleRoomsAll sweeps every room shard for stale, empty rooms. Callers
// must not hold a shard lock: it acquires one shard at a time, so it can never
// deadlock against another concurrent global sweep.
func evictStaleRoomsAll() int {
	total := 0
	for i := range shards {
		sh := &shards[i]
		sh.mu.Lock()
		total += evictStaleRooms(sh)
		sh.mu.Unlock()
	}
	return total
}

func cleanupRegs(ctx context.Context) {
	rateTick := time.NewTicker(rateMapCleanupInterval)
	defer rateTick.Stop()
	roomTick := time.NewTicker(regCleanupInterval)
	defer roomTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rateTick.C:
			for i := range regShards {
				sh := &regShards[i]
				sh.mu.Lock()
				for src, reset := range sh.times {
					if time.Since(reset) > rateWindow*2 {
						delete(sh.sent, src)
						delete(sh.times, src)
					}
				}
				sh.mu.Unlock()
			}
			dropLogLimiter.cleanupStale(dropLogInterval * 2)
		case <-roomTick.C:
			evictStaleRoomsAll()
			removeStalePendingRegs(time.Now())
		}
	}
}
