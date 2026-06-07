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
	defaultMaxClientsPerRoom  = 2
	defaultMaxRoomsPerIP      = 100
	defaultMaxMsgRate         = 10
	defaultRateWindow         = 1 * time.Second
	defaultRegCleanupInterval = 1 * time.Minute
	defaultRegTTL             = 5 * time.Minute
	defaultRelayWorkers       = 4
)

var (
	maxRooms           int           = defaultMaxRooms
	maxClientsPerRoom  int           = defaultMaxClientsPerRoom
	maxRoomsPerIP      int           = defaultMaxRoomsPerIP
	maxMsgRate         int           = defaultMaxMsgRate
	rateWindow         time.Duration = defaultRateWindow
	regCleanupInterval time.Duration = defaultRegCleanupInterval
	regTTL             time.Duration = defaultRegTTL
	relayWorkers       int           = defaultRelayWorkers
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
	mu         sync.Mutex
	udpClients map[string]*udpClient
	t          time.Time
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

var (
	shards         [numShards]roomShard
	regShards      [numRegShards]regLimitShard
	totalRoomCount atomic.Int64
	ipCounts       [numShards]ipCountShard

	statRegs  atomic.Int64
	statMsgs  atomic.Int64
	statDrops atomic.Int64

	dropLogLimiter = new(ipRateLimiter)
)

type ipRateLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (l *ipRateLimiter) allow(ip string, interval time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if last, ok := l.last[ip]; ok && now.Sub(last) < interval {
		return false
	}
	if l.last == nil {
		l.last = make(map[string]time.Time)
	}
	l.last[ip] = now
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
	maxClientsPerRoom = util.EnvInt("QVOLE_RELAY_MAX_CLIENTS", defaultMaxClientsPerRoom)
	maxRoomsPerIP = util.EnvInt("QVOLE_RELAY_MAX_ROOMS_PER_IP", defaultMaxRoomsPerIP)
	maxMsgRate = util.EnvInt("QVOLE_RELAY_MSG_RATE", defaultMaxMsgRate)
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
}

func shardIdx(key string, n uint32) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32() % n
}

func shardFor(room string) *roomShard         { return &shards[shardIdx(room, numShards)] }
func regShardFor(src string) *regLimitShard   { return &regShards[shardIdx(src, numRegShards)] }
func ipCountShardFor(ip string) *ipCountShard { return &ipCounts[shardIdx(ip, numShards)] }

func ipKey(src string) string {
	host, _, err := net.SplitHostPort(src)
	if err != nil {
		return src
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
	if !ok || now.Sub(reset) > rateWindow {
		sh.sent[src] = 1
		sh.times[src] = now
		return false
	}
	if sh.sent[src] >= maxMsgRate {
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

func countIPRooms(ip string) int {
	sh := ipCountShardFor(ip)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return len(sh.rooms[ip])
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

func evictStaleRooms(shard *roomShard) int {
	now := time.Now()
	evicted := 0
	for room, entry := range shard.rooms {
		entry.mu.Lock()
		entry.removeStaleClients(room, now)
		empty := len(entry.udpClients) == 0
		stale := now.Sub(entry.t) > regTTL
		entry.mu.Unlock()
		if empty && stale {
			delete(shard.rooms, room)
			totalRoomCount.Add(-1)
			evicted++
		}
	}
	return evicted
}

func cleanupRegs(ctx context.Context) {
	t := time.NewTicker(regCleanupInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
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
			for i := range shards {
				shard := &shards[i]
				shard.mu.Lock()
				evictStaleRooms(shard)
				shard.mu.Unlock()
			}
		}
	}
}
