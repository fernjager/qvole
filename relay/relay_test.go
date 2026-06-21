package relay

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fernjager/qvole/internal/engine"
	"github.com/fernjager/qvole/internal/util"
	"github.com/fernjager/qvole/spake2"
)

func TestValidRoomName_Valid(t *testing.T) {
	exact64 := ""
	for i := 0; i < maxRoomNameLen; i++ {
		exact64 += "a"
	}
	tests := []string{"1234", "abcd", "room-42", "AZ", string(rune(33)), string(rune(126)), exact64}
	for _, tt := range tests {
		if !validRoomName(tt) {
			t.Errorf("expected valid for %q", tt)
		}
	}
}

func TestValidRoomName_TooLong(t *testing.T) {
	room := ""
	for i := 0; i <= maxRoomNameLen; i++ {
		room += "a"
	}
	if validRoomName(room) {
		t.Errorf("expected invalid for room of length %d", len(room))
	}
}

func TestValidRoomName_Empty(t *testing.T) {
	if validRoomName("") {
		t.Fatal("expected invalid for empty room name")
	}
}

func TestValidRoomName_NonPrintable(t *testing.T) {
	tests := []string{"room\x00", "test\x7f", "abc\n", "\troom", "a\x1b", "room with space"}
	for _, tt := range tests {
		if validRoomName(tt) {
			t.Errorf("expected invalid for %q (non-printable)", tt)
		}
	}
}

func TestValidRoomName_OutOfRange(t *testing.T) {
	if validRoomName(string(rune(31))) {
		t.Error("expected invalid for char 31")
	}
	if validRoomName(string(rune(32))) {
		t.Error("expected invalid for char 32 (space, excluded from room names)")
	}
	if !validRoomName(string(rune(33))) {
		t.Error("expected valid for char 33 (!)")
	}
	if !validRoomName(string(rune(126))) {
		t.Error("expected valid for char 126 (~)")
	}
	if validRoomName(string(rune(127))) {
		t.Error("expected invalid for char 127 (DEL)")
	}
}

func TestIsRateLimited_UnderLimit(t *testing.T) {
	info := &udpClient{rateLimiter: rateLimiter{windowStart: time.Now()}}
	for i := 0; i < maxMsgRate; i++ {
		if isMsgRateLimited(info) {
			t.Fatalf("rate limited at %d, window max is %d", i+1, maxMsgRate)
		}
	}
}

func TestIsRateLimited_OverLimit(t *testing.T) {
	info := &udpClient{rateLimiter: rateLimiter{windowStart: time.Now()}}
	for i := 0; i < maxMsgRate; i++ {
		isMsgRateLimited(info)
	}
	if !isMsgRateLimited(info) {
		t.Fatal("expected rate limited after exceeding maxMsgRate")
	}
}

func TestIsRateLimited_WindowReset(t *testing.T) {
	info := &udpClient{rateLimiter: rateLimiter{windowStart: time.Now()}}
	for i := 0; i < maxMsgRate; i++ {
		isMsgRateLimited(info)
	}

	info.windowStart = time.Now().Add(-2 * rateWindow)
	if isMsgRateLimited(info) {
		t.Fatal("expected not rate limited after window reset")
	}
}

func TestIsRateLimited_ZeroWindow(t *testing.T) {
	info := &udpClient{rateLimiter: rateLimiter{windowStart: time.Time{}}}
	for i := 0; i < maxMsgRate; i++ {
		isMsgRateLimited(info)
	}
	if !isMsgRateLimited(info) {
		t.Fatal("expected rate limited at limit even with zero start")
	}
}

func TestIsRateLimited_AfterWindowElapsed(t *testing.T) {
	info := &udpClient{rateLimiter: rateLimiter{windowStart: time.Now().Add(-2 * rateWindow)}}
	if isMsgRateLimited(info) {
		t.Fatal("should not be limited after window has elapsed")
	}
	for i := 0; i < maxMsgRate; i++ {
		isMsgRateLimited(info)
	}
	if !isMsgRateLimited(info) {
		t.Fatal("should be limited after exhausting fresh window")
	}
}

func TestClientInfo_LastSeen(t *testing.T) {
	now := time.Now()
	info := &udpClient{lastSeen: now}
	if !info.lastSeen.Equal(now) {
		t.Fatal("lastSeen should match set time")
	}
}

func TestIsValidHex_Valid(t *testing.T) {
	cases := []string{"ff", "FF", "aAbB", "00", "deadbeef", "0123456789abcdefABCDEF"}
	for _, c := range cases {
		if !isValidHex(c) {
			t.Errorf("expected valid hex: %q", c)
		}
	}
}

func TestIsValidHex_Invalid(t *testing.T) {
	cases := []string{"", "f", "gg", "0x12", "hello", "abcg", "ff ", " ff"}
	for _, c := range cases {
		if isValidHex(c) {
			t.Errorf("expected invalid hex: %q", c)
		}
	}
}

func TestShardFor_Deterministic(t *testing.T) {
	rooms := []string{"0000", "1234", "abcd", "room-42", ""}
	for _, room := range rooms {
		s1 := shardFor(room)
		s2 := shardFor(room)
		if s1 != s2 {
			t.Errorf("shardFor(%q) not deterministic: %v vs %v", room, s1, s2)
		}
	}
}

func TestTotalRooms_Zero(t *testing.T) {
	n := totalRooms()
	if n < 0 {
		t.Fatalf("totalRooms returned negative: %d", n)
	}
}

func TestIPKey_CanonicalizesIPv6(t *testing.T) {
	// IPv6 literals in host:port form must be bracketed.
	variants := []string{
		"[2001:db8::1]:1234",
		"[2001:db8:0:0:0:0:0:1]:1234",
		"[2001:0db8:0000:0000:0000:0000:0000:0001]:1234",
	}
	keys := make([]string, len(variants))
	for i, v := range variants {
		keys[i] = ipKey(v)
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] != keys[0] {
			t.Errorf("ipv6 variant %q produced different key %q (want %q)", variants[i], keys[i], keys[0])
		}
	}
}

func TestIPKey_CanonicalizesIPv4MappedIPv6(t *testing.T) {
	v6 := ipKey("[::ffff:1.2.3.4]:5678")
	v4 := ipKey("1.2.3.4:5678")
	if v6 != v4 {
		t.Errorf("ipv4-mapped ipv6 not collapsed: v6=%q v4=%q", v6, v4)
	}
}

func TestIPKey_PassesHostname(t *testing.T) {
	if got := ipKey("example.com:80"); got != "example.com" {
		t.Errorf("hostname changed: got %q want example.com", got)
	}
}

func TestIPKey_FallbackOnParseError(t *testing.T) {
	// No port: SplitHostPort errors, should return input unchanged.
	in := "not-a-valid-hostport"
	if got := ipKey(in); got != in {
		t.Errorf("expected fallback to input, got %q", got)
	}
}

func TestRelayConstants(t *testing.T) {
	if maxRooms != 10000 {
		t.Errorf("maxRooms = %d, want 10000", maxRooms)
	}
	if maxMsgRate != 10 {
		t.Errorf("maxMsgRate = %d, want 10", maxMsgRate)
	}
	if maxDatagramLen != 1400 {
		t.Errorf("maxDatagramLen = %d, want 1400", maxDatagramLen)
	}
	if regTTL != 1*time.Minute {
		t.Errorf("regTTL = %v, want 1m", regTTL)
	}
	if regCleanupInterval != 1*time.Minute {
		t.Errorf("regCleanupInterval = %v, want 1m", regCleanupInterval)
	}
	if maxRoomsPerIP != 10 {
		t.Errorf("maxRoomsPerIP = %d, want 10", maxRoomsPerIP)
	}
	if numRegShards != 16 {
		t.Errorf("numRegShards = %d, want 16", numRegShards)
	}
	if maxClientsHard != 20 {
		t.Errorf("maxClientsHard = %d, want 20", maxClientsHard)
	}
}

func cleanupRegLimit(src string) {
	sh := regShardFor(src)
	sh.mu.Lock()
	delete(sh.sent, src)
	delete(sh.times, src)
	sh.mu.Unlock()
}

func TestIsRegRateLimited_UnderLimit(t *testing.T) {
	src := "test-src"
	defer cleanupRegLimit(src)
	for i := 0; i < maxMsgRate; i++ {
		if isRegRateLimited(src) {
			t.Fatalf("reg rate limited at call %d, max is %d", i+1, maxMsgRate)
		}
	}
}

func TestIsRegRateLimited_OverLimit(t *testing.T) {
	src := "test-src"
	defer cleanupRegLimit(src)
	for i := 0; i < maxMsgRate; i++ {
		isRegRateLimited(src)
	}
	if !isRegRateLimited(src) {
		t.Fatal("expected reg rate limited after exceeding maxMsgRate")
	}
}

func TestIsRegRateLimited_SeparateSources(t *testing.T) {
	src1 := "src-1"
	defer cleanupRegLimit(src1)
	src2 := "src-2"
	defer cleanupRegLimit(src2)
	for i := 0; i < maxMsgRate; i++ {
		isRegRateLimited(src1)
		if isRegRateLimited(src2) {
			t.Fatalf("src2 rate limited after src1 activity")
		}
	}
}

func TestIsRegRateLimited_WindowReset(t *testing.T) {
	src := "test-src"
	defer cleanupRegLimit(src)
	for i := 0; i < maxMsgRate; i++ {
		isRegRateLimited(src)
	}
	if !isRegRateLimited(src) {
		t.Fatal("expected rate limited")
	}
	sh := regShardFor(src)
	sh.mu.Lock()
	sh.times[src] = time.Now().Add(-2 * rateWindow)
	sh.mu.Unlock()
	if isRegRateLimited(src) {
		t.Fatal("expected not rate limited after window reset")
	}
}

func TestIsRegRateLimited_FirstCall(t *testing.T) {
	src := "new-src"
	defer cleanupRegLimit(src)
	if isRegRateLimited(src) {
		t.Fatal("first call should not be rate limited")
	}
}

func TestExchangeConstants(t *testing.T) {
	if engine.ExchangeDeadline != 90*time.Second {
		t.Errorf("exchangeDeadline = %v, want 90s", engine.ExchangeDeadline)
	}
	if engine.RegInterval != 30*time.Second {
		t.Errorf("regInterval = %v, want 30s", engine.RegInterval)
	}
}

func TestBufferPool(t *testing.T) {
	buf := engine.BufferPool.Get().([]byte)
	if cap(buf) < 32*1024 {
		t.Errorf("BufferPool buffer cap = %d, want >= %d", cap(buf), 32*1024)
	}
	if len(buf) < 32*1024 {
		t.Errorf("BufferPool buffer len = %d, want >= %d", len(buf), 32*1024)
	}
	engine.BufferPool.Put(buf)
}

func TestCodeWordsQuality(t *testing.T) {
	if len(spake2.CodeWords) != 7766 {
		t.Fatalf("expected 7766 words, got %d", len(spake2.CodeWords))
	}
	seen := make(map[string]bool, len(spake2.CodeWords))
	for _, w := range spake2.CodeWords {
		if w == "" {
			t.Error("codeWords contains empty string")
		}
		if seen[w] {
			t.Errorf("codeWords contains duplicate: %q", w)
		}
		seen[w] = true
		for _, c := range w {
			if c == '-' {
				continue
			}
			if c < 'a' || c > 'z' {
				t.Errorf("codeWords entry %q contains non-lowercase char %q", w, c)
			}
		}
		if len(w) > 0 && w[0] == '-' {
			t.Errorf("codeWords entry %q has leading hyphen", w)
		}
		if len(w) > 0 && w[len(w)-1] == '-' {
			t.Errorf("codeWords entry %q has trailing hyphen", w)
		}
	}
}

func TestSPAKE2DomainStrings(t *testing.T) {
	pw := spake2.PasswordToScalar("test")
	if pw.Sign() == 0 {
		t.Fatal("PasswordToScalar returned zero")
	}
	x, y := spake2.HashToCurve(spake2.Curve, []byte("qvole-spake2-M-v1"))
	if !spake2.Curve.IsOnCurve(x, y) {
		t.Fatal("generator M is not on curve")
	}
	x2, y2 := spake2.HashToCurve(spake2.Curve, []byte("qvole-spake2-M-v1"))
	if x.Cmp(x2) != 0 || y.Cmp(y2) != 0 {
		t.Fatal("generator M is not deterministic")
	}
	nx, ny := spake2.HashToCurve(spake2.Curve, []byte("qvole-spake2-N-v1"))
	if !spake2.Curve.IsOnCurve(nx, ny) {
		t.Fatal("generator N is not on curve")
	}
	nx2, ny2 := spake2.HashToCurve(spake2.Curve, []byte("qvole-spake2-N-v1"))
	if nx.Cmp(nx2) != 0 || ny.Cmp(ny2) != 0 {
		t.Fatal("generator N is not deterministic")
	}
	if x.Cmp(nx) == 0 && y.Cmp(ny) == 0 {
		t.Fatal("generators M and N should be distinct")
	}
	np := util.Nameplate("")
	if np == "" {
		t.Fatal("Nameplate returned empty for empty input")
	}
}

func TestIsRateLimited_Concurrent(t *testing.T) {
	info := &udpClient{rateLimiter: rateLimiter{windowStart: time.Now()}}
	var mu sync.Mutex
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < maxMsgRate; j++ {
				mu.Lock()
				isMsgRateLimited(info)
				mu.Unlock()
			}
			errs <- nil
		}()
	}
	for i := 0; i < 10; i++ {
		<-errs
	}
	mu.Lock()
	limited := isMsgRateLimited(info)
	mu.Unlock()
	if !limited {
		t.Log("rate limiter should be at limit after concurrent calls")
	}
}

func TestIPKey(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"1.2.3.4:5678", "1.2.3.4"},
		{"[::1]:9009", "::1"},
		{"no-port", "no-port"},
	}
	for _, tt := range tests {
		got := ipKey(tt.input)
		if got != tt.want {
			t.Errorf("ipKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIPRateLimiter_CleanupStale(t *testing.T) {
	l := new(ipRateLimiter)

	l.mu.Lock()
	l.last = make(map[string]time.Time)
	l.last["stale-ip"] = time.Now().Add(-10 * time.Second)
	l.last["fresh-ip"] = time.Now()
	l.mu.Unlock()

	l.cleanupStale(5 * time.Second)

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.last["stale-ip"]; exists {
		t.Error("expected stale-ip to be cleaned up")
	}
	if _, exists := l.last["fresh-ip"]; !exists {
		t.Error("expected fresh-ip to be retained")
	}
}

func TestAddRemoveIPRoom(t *testing.T) {
	resetIPCounts()
	ip := "10.0.0.1"

	addIPRoom(ip, "room-a")
	if n := countIPRooms(ip); n != 1 {
		t.Fatalf("countIPRooms = %d, want 1", n)
	}

	addIPRoom(ip, "room-b")
	if n := countIPRooms(ip); n != 2 {
		t.Fatalf("countIPRooms = %d, want 2", n)
	}

	addIPRoom(ip, "room-a")
	if n := countIPRooms(ip); n != 2 {
		t.Fatalf("countIPRooms after duplicate add = %d, want 2", n)
	}

	removeIPRoom(ip, "room-a")
	if n := countIPRooms(ip); n != 1 {
		t.Fatalf("countIPRooms after remove = %d, want 1", n)
	}

	removeIPRoom(ip, "room-b")
	if n := countIPRooms(ip); n != 0 {
		t.Fatalf("countIPRooms after remove all = %d, want 0", n)
	}
}

func TestRemoveIPRoom_Nonexistent(t *testing.T) {
	resetIPCounts()
	removeIPRoom("99.99.99.99", "no-such-room")
	if n := countIPRooms("99.99.99.99"); n != 0 {
		t.Fatalf("countIPRooms = %d, want 0", n)
	}
}

func TestTotalClients(t *testing.T) {
	if n := totalClients(); n < 0 {
		t.Fatalf("totalClients = %d, want >= 0", n)
	}
}

func TestHandleReg_RoomCapacityLimit(t *testing.T) {
	relayConn, _, _ := setupRelayConns(t)

	saved := totalRoomCount.Load()
	totalRoomCount.Store(int64(maxRooms))
	defer totalRoomCount.Store(saved)

	room := uniqueRoom("reg-cap-limit")
	clientConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client: %v", err)
	}
	defer clientConn.Close()
	clientAddr := clientConn.LocalAddr().(*net.UDPAddr)

	HandlePacket(relayConn, []byte("REG "+room+"\n"), clientAddr)

	clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err = clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no REGD when room capacity reached")
	}
}

func TestHandleMsg_NilResolvedAddr(t *testing.T) {
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	room := uniqueRoom("msg-nil-addr")

	HandlePacket(relayConn, []byte("REG "+room+"\n"), client1Addr)
	readUDPLine(t, client1Conn, 500*time.Millisecond)

	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	HandlePacket(relayConn, []byte("REG "+room+"\n"), client2Addr)
	readUDPLine(t, client2Conn, 500*time.Millisecond)

	shard := shardFor(room)
	shard.mu.RLock()
	entry := shard.rooms[room]
	shard.mu.RUnlock()

	entry.mu.Lock()
	for addr, ci := range entry.udpClients {
		if addr == client1Addr.String() {
			ci.resolvedAddr = nil
		}
	}
	entry.mu.Unlock()

	HandlePacket(relayConn, []byte("MSG "+room+" spake2 deadbeef\n"), client2Addr)

	time.Sleep(50 * time.Millisecond)
}

func TestIsRegRateLimited_Cleanup(t *testing.T) {
	src := "cleanup-test-src"
	defer cleanupRegLimit(src)
	for i := 0; i < maxMsgRate; i++ {
		isRegRateLimited(src)
	}

	if !isRegRateLimited(src) {
		t.Fatal("expected rate limited")
	}

	sh := regShardFor(src)
	sh.mu.Lock()
	sh.times[src] = time.Now().Add(-3 * rateWindow)
	sh.mu.Unlock()

	for s, reset := range sh.times {
		if time.Since(reset) > rateWindow*2 {
			delete(sh.sent, s)
			delete(sh.times, s)
		}
	}

	if isRegRateLimited(src) {
		t.Fatal("expected not rate limited after cleanup")
	}
}

func TestHandlePacket_NonPrefixCommand(t *testing.T) {
	relayConn, clientConn, clientAddr := setupRelayConns(t)

	HandlePacket(relayConn, []byte("PING room\n"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for non-prefix command")
	}

	HandlePacket(relayConn, []byte("GET / HTTP/1.1\n"), clientAddr)
	clientConn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	_, err = clientConn.Read(buf)
	if err == nil {
		t.Fatal("expected no response for HTTP-like command")
	}
}

func TestHandlePacket_ConcurrentRegAndMsg(t *testing.T) {
	relayConn, client1Conn, client1Addr := setupRelayConns(t)
	client2Conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen client2: %v", err)
	}
	defer client2Conn.Close()
	client2Addr := client2Conn.LocalAddr().(*net.UDPAddr)

	room := uniqueRoom("concurrent-reg-msg")

	registerClient(t, relayConn, client1Conn, client1Addr, room)
	registerClient(t, relayConn, client2Conn, client2Addr, room)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			HandlePacket(relayConn, []byte(fmt.Sprintf("MSG %s spake2 %02x\n", room, i)), client1Addr)
			time.Sleep(time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			HandlePacket(relayConn, []byte(fmt.Sprintf("MSG %s confirm %02x\n", room, i+100)), client2Addr)
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()

	received := 0
	for {
		client1Conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buf := make([]byte, 1500)
		_, err := client1Conn.Read(buf)
		if err != nil {
			break
		}
		received++
	}
	for {
		client2Conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buf := make([]byte, 1500)
		_, err := client2Conn.Read(buf)
		if err != nil {
			break
		}
		received++
	}
	if received == 0 {
		t.Fatal("expected at least some messages from concurrent processing")
	}
}
