package qvole

import (
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

type quicStreamConn struct {
	stream *quic.Stream
	conn   *quic.Conn
	laddr  net.Addr
	raddr  net.Addr
}

func (c *quicStreamConn) Read(b []byte) (int, error)         { return (*c.stream).Read(b) }
func (c *quicStreamConn) Write(b []byte) (int, error)        { return (*c.stream).Write(b) }
func (c *quicStreamConn) LocalAddr() net.Addr                { return c.laddr }
func (c *quicStreamConn) RemoteAddr() net.Addr               { return c.raddr }
func (c *quicStreamConn) SetDeadline(t time.Time) error      { return (*c.stream).SetDeadline(t) }
func (c *quicStreamConn) SetReadDeadline(t time.Time) error  { return (*c.stream).SetReadDeadline(t) }
func (c *quicStreamConn) SetWriteDeadline(t time.Time) error { return (*c.stream).SetWriteDeadline(t) }

func (c *quicStreamConn) CloseWrite() error {
	return (*c.stream).Close()
}

// streamCloseDelay is a brief grace period between closing the stream and
// closing the QUIC connection: once CONNECTION_CLOSE is sent, quic-go does not
// guarantee delivery of unacknowledged stream data (mirrors pipeFinishDelay in
// internal/engine, which is unexported).
const streamCloseDelay = 500 * time.Millisecond

func (c *quicStreamConn) Close() error {
	err := (*c.stream).Close()
	time.Sleep(streamCloseDelay)
	c.conn.CloseWithError(0, "normal close")
	return err
}
