package engine

import (
	"bytes"
	"testing"
)

func TestBufferPool_ReturnsCorrectSize(t *testing.T) {
	buf := BufferPool.Get().([]byte)
	if len(buf) != 32*1024 {
		t.Fatalf("expected 32KB buffer, got %d bytes", len(buf))
	}
	BufferPool.Put(buf)
}

func TestBufferPool_Capacity(t *testing.T) {
	buf := BufferPool.Get().([]byte)
	if cap(buf) < 32*1024 {
		t.Fatalf("capacity %d < 32KB", cap(buf))
	}
	BufferPool.Put(buf)
}

func TestPutBuffer_ZerosContents(t *testing.T) {
	buf := BufferPool.Get().([]byte)
	// fill with non-zero data
	for i := range buf {
		buf[i] = 0xFF
	}
	PutBuffer(buf)

	buf2 := BufferPool.Get().([]byte)
	if !bytes.Equal(buf2, make([]byte, 32*1024)) {
		t.Fatal("PutBuffer did not zero the buffer contents")
	}
	BufferPool.Put(buf2)
}

func TestPutBuffer_Nil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PutBuffer(nil) panicked: %v", r)
		}
	}()
	PutBuffer(nil)
}

func TestPutBuffer_ReturnsToPool(t *testing.T) {
	buf := BufferPool.Get().([]byte)
	PutBuffer(buf)
	// should be able to get another buffer without panic
	buf2 := BufferPool.Get().([]byte)
	BufferPool.Put(buf2)
}
