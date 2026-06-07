package engine

import (
	"sync"

	"github.com/fernjager/qvole/spake2"
)

const bufferPoolSize = 32 * 1024

// BufferPool provides reusable 32 KB buffers for bulk data copy operations.
// Buffers are zeroed before being returned to the pool.
var BufferPool = &sync.Pool{
	New: func() any {
		return make([]byte, bufferPoolSize)
	},
}

// PutBuffer zeroes the buffer and returns it to BufferPool.
func PutBuffer(buf []byte) {
	spake2.ZeroBytes(buf)
	BufferPool.Put(buf)
}
