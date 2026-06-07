package engine

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1048576, "1.00 MB"},
		{1572864, "1.50 MB"},
		{1073741824, "1.00 GB"},
		{1610612736, "1.50 GB"},
	}
	for _, tt := range tests {
		got := formatBytes(tt.n)
		if !strings.HasPrefix(got, strings.Split(tt.want, " ")[0]) {
			t.Errorf("formatBytes(%d) = %q, want prefix of %q", tt.n, got, tt.want)
		}
		if !strings.Contains(got, "B") {
			t.Errorf("formatBytes(%d) = %q, missing unit", tt.n, got)
		}
	}
}

func TestFormatBytesRate(t *testing.T) {
	tests := []struct {
		rate float64
		unit string
	}{
		{0, "B/s"},
		{500, "B/s"},
		{1024, "KB/s"},
		{1048576, "MB/s"},
		{1073741824, "GB/s"},
	}
	for _, tt := range tests {
		got := formatBytesRate(tt.rate)
		if !strings.Contains(got, tt.unit) {
			t.Errorf("formatBytesRate(%f) = %q, want unit %q", tt.rate, got, tt.unit)
		}
	}
}

func TestDirectionWriter(t *testing.T) {
	var buf bytes.Buffer
	dw := &directionWriter{w: &buf, counter: &atomic.Int64{}}

	data := []byte("hello")
	n, err := dw.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(data) {
		t.Fatalf("Write returned %d, want %d", n, len(data))
	}
	if dw.counter.Load() != int64(len(data)) {
		t.Errorf("counter = %d, want %d", dw.counter.Load(), len(data))
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Errorf("buffer = %q, want %q", buf.Bytes(), data)
	}
}

func TestDirectionWriter_MultipleWrites(t *testing.T) {
	var buf bytes.Buffer
	dw := &directionWriter{w: &buf, counter: &atomic.Int64{}}

	dw.Write([]byte("abc"))
	dw.Write([]byte("def"))
	dw.Write([]byte("ghi"))

	if dw.counter.Load() != 9 {
		t.Errorf("counter = %d, want 9", dw.counter.Load())
	}
}

func TestStatsTracker_TXRX(t *testing.T) {
	st := NewStatsTracker()
	var txBuf bytes.Buffer
	var rxBuf bytes.Buffer

	txW := st.TXWriter(&txBuf)
	rxW := st.RXWriter(&rxBuf)

	txW.Write([]byte("tx-data"))
	rxW.Write([]byte("rx-data"))

	if st.tx.Load() != 7 {
		t.Errorf("tx = %d, want 7", st.tx.Load())
	}
	if st.rx.Load() != 7 {
		t.Errorf("rx = %d, want 7", st.rx.Load())
	}
}

func TestStatsTracker_StopAndLog(t *testing.T) {
	st := NewStatsTracker()
	st.StopAndLog()
}

func TestDirectionWriter_Concurrent(t *testing.T) {
	var mu sync.Mutex
	var written int
	w := &threadsafeWriter{mu: &mu, written: &written}
	dw := &directionWriter{w: w, counter: &atomic.Int64{}}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dw.Write([]byte("x"))
		}()
	}
	wg.Wait()
	if dw.counter.Load() != 100 {
		t.Errorf("counter = %d, want 100", dw.counter.Load())
	}
}

type threadsafeWriter struct {
	mu      *sync.Mutex
	written *int
}

func (w *threadsafeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.written += len(p)
	return len(p), nil
}
