package engine

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/fernjager/qvole/internal/util"
)

type directionWriter struct {
	w       io.Writer
	counter *atomic.Int64
}

func (dw *directionWriter) Write(p []byte) (int, error) {
	n, err := dw.w.Write(p)
	dw.counter.Add(int64(n))
	return n, err
}

// StatsTracker tracks TX/RX byte counts for throughput reporting.
type StatsTracker struct {
	tx   atomic.Int64
	rx   atomic.Int64
	done chan struct{}
}

// NewStatsTracker creates a new StatsTracker ready for use.
func NewStatsTracker() *StatsTracker {
	return &StatsTracker{done: make(chan struct{})}
}

// TXWriter wraps w to count transmitted bytes.
func (st *StatsTracker) TXWriter(w io.Writer) io.Writer {
	return &directionWriter{w: w, counter: &st.tx}
}

// RXWriter wraps w to count received bytes.
func (st *StatsTracker) RXWriter(w io.Writer) io.Writer {
	return &directionWriter{w: w, counter: &st.rx}
}

// Start begins periodic throughput logging at the given interval.
func (st *StatsTracker) Start(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var lastTX, lastRX int64
		lastTime := time.Now()
		first := true

		for {
			select {
			case <-st.done:
				return
			case t := <-ticker.C:
				tx := st.tx.Load()
				rx := st.rx.Load()
				elapsed := t.Sub(lastTime).Seconds()

				if first {
					first = false
					lastTX, lastRX = tx, rx
					lastTime = t
					continue
				}

				txDelta := tx - lastTX
				rxDelta := rx - lastRX

				if txDelta > 0 || rxDelta > 0 {
					util.LogPipe.PrintfInfo("↑ %s (%s)  ↓ %s (%s)",
						formatBytes(tx), util.Bold(formatBytesRate(float64(txDelta)/elapsed)),
						formatBytes(rx), util.Bold(formatBytesRate(float64(rxDelta)/elapsed)))
				}
				lastTX, lastRX = tx, rx
				lastTime = t
			}
		}
	}()
}

// StopAndLog stops the periodic logger and prints the final totals.
func (st *StatsTracker) StopAndLog() {
	close(st.done)
	tx := st.tx.Load()
	rx := st.rx.Load()
	if tx > 0 || rx > 0 {
		util.LogPipe.PrintfInfo("Total: ↑ %s  ↓ %s", util.Bold(formatBytes(tx)), util.Bold(formatBytes(rx)))
	} else {
		util.LogPipe.PrintfSuccess("Done!")
	}
}

func formatBytesValue(val float64, suffix string, exactInt bool) string {
	switch {
	case val >= 1<<30:
		return fmt.Sprintf("%.2f G%s", val/(1<<30), suffix)
	case val >= 1<<20:
		return fmt.Sprintf("%.2f M%s", val/(1<<20), suffix)
	case val >= 1<<10:
		return fmt.Sprintf("%.2f K%s", val/(1<<10), suffix)
	default:
		if exactInt {
			return fmt.Sprintf("%d %s", int64(val), suffix)
		}
		return fmt.Sprintf("%.0f %s", val, suffix)
	}
}

func formatBytes(n int64) string {
	return formatBytesValue(float64(n), "B", true)
}

func formatBytesRate(r float64) string {
	return formatBytesValue(r, "B/s", false)
}
