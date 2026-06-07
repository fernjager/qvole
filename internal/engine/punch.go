package engine

import (
	"context"
	"crypto/rand"
	"net"
	"time"

	"github.com/fernjager/qvole/internal/util"
)

const (
	punchBufferSize     = 1600
	punchReadDeadline   = 100 * time.Millisecond
	punchLogInterval    = 50
	punchIntervalChange = 5
)

var (
	punchIntervals    = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond}
	punchPayloadSizes = []int{5, 50, 100, 200}
)

func listenForPunch(ctx context.Context, udpConn *net.UDPConn, peerAddr *net.UDPAddr, recvCh chan<- *net.UDPAddr) {
	buf := make([]byte, punchBufferSize)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		udpConn.SetReadDeadline(time.Now().Add(punchReadDeadline))
		n, addr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			continue
		}
		if n <= 0 {
			continue
		}
		if peerAddr != nil {
			if !addr.IP.Equal(peerAddr.IP) {
				continue
			}
		}
		util.LogHole.Printf("Punch packet received from %s", addr)
		select {
		case recvCh <- addr:
		default:
		}
		return
	}
}

func startHolePunching(ctx context.Context, udpConn *net.UDPConn, peerAddr *net.UDPAddr) <-chan *net.UDPAddr {
	successCh := make(chan *net.UDPAddr, 1)
	go func() {
		defer close(successCh)
		recvCh := make(chan *net.UDPAddr, 1)
		go listenForPunch(ctx, udpConn, peerAddr, recvCh)

		payloads := make([][]byte, len(punchPayloadSizes))
		for i, sz := range punchPayloadSizes {
			payloads[i] = make([]byte, sz)
			if _, err := rand.Read(payloads[i]); err != nil {
				return
			}
		}

		ticker := time.NewTicker(punchIntervals[0])
		defer ticker.Stop()
		attempt := 0
		intervalIdx := 0
		util.LogHole.Printf("Hole punching to %s", peerAddr)

		for {
			select {
			case <-ctx.Done():
				return
			case addr := <-recvCh:
				select {
				case successCh <- addr:
				default:
				}
				return
			case <-ticker.C:
				payload := payloads[attempt%len(payloads)]
				udpConn.WriteTo(payload, peerAddr) // best-effort; failures are expected during hole punch
				attempt++
				if attempt%punchLogInterval == 0 {
					util.LogHole.Printf("Hole punch attempt %d, interval %v", attempt, punchIntervals[intervalIdx])
				}
				if attempt > 0 && attempt%punchIntervalChange == 0 && intervalIdx < len(punchIntervals)-1 {
					intervalIdx++
					ticker.Reset(punchIntervals[intervalIdx])
				}
			}
		}
	}()
	return successCh
}
