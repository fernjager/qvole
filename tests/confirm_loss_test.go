package tests

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/fernjager/qvole"
	"github.com/fernjager/qvole/relay"
)

// TestConnect_SurvivesLostFinalConfirm is the regression test for the lost
// final confirm: a peer stops answering the moment the other side's confirm
// arrives, so if every confirm it sent before then is lost, the other side
// never completes the exchange. The relay here drops all confirms from the
// first peer to send one for a 3 s burst (long enough to cover everything it
// sends before finishing), then forwards normally. Before the fix this failed
// every time: one peer timed out in the exchange, the other in QUIC.
func TestConnect_SurvivesLostFinalConfirm(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	defer conn.Close()

	go func() {
		var victim string
		var burstStart time.Time
		buf := make([]byte, testBufSize)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			data := append([]byte(nil), buf[:n]...)
			if bytes.HasPrefix(data, []byte("MSG ")) && bytes.Contains(data, []byte(" confirm ")) {
				if victim == "" {
					victim, burstStart = src.String(), time.Now()
				}
				if src.String() == victim && time.Since(burstStart) < 3*time.Second {
					continue
				}
			}
			relay.HandlePacket(conn, data, src)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	opts := []qvole.Option{
		qvole.WithCode(fmt.Sprintf("lost-confirm-%d", time.Now().UnixNano())),
		qvole.WithRelay(conn.LocalAddr().String()),
		qvole.WithExchangeDeadline(15 * time.Second),
		qvole.WithPunchTimeout(3 * time.Second),
		qvole.WithHandshakeTimeout(10 * time.Second),
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, _, err := qvole.Connect(ctx, opts...)
			if err == nil {
				defer c.CloseWithError(0, "")
			}
			errs[i] = err
		}(i)
		time.Sleep(300 * time.Millisecond)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("peer %d: %v", i, err)
		}
	}
}
