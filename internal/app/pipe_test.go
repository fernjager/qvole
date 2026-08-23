package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

func TestBidirectionalCopy_TransfersBothDirections(t *testing.T) {
	aReader := strings.NewReader("hello from a")
	aWriter := &bytes.Buffer{}
	bReader := strings.NewReader("hello from b")
	bWriter := &bytes.Buffer{}

	a := struct {
		io.Reader
		io.Writer
	}{aReader, aWriter}
	b := struct {
		io.Reader
		io.Writer
	}{bReader, bWriter}

	bidirectionalCopy(a, b)

	if !strings.Contains(bWriter.String(), "hello from a") {
		t.Fatalf("b did not receive data from a: %q", bWriter.String())
	}
	if !strings.Contains(aWriter.String(), "hello from b") {
		t.Fatalf("a did not receive data from b: %q", aWriter.String())
	}
}

type closerPair struct {
	io.Reader
	io.Writer
	closed bool
}

func (cp *closerPair) Close() error {
	cp.closed = true
	return nil
}

func TestBidirectionalCopy_ClosesBoth(t *testing.T) {
	a := &closerPair{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}}
	b := &closerPair{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}}

	bidirectionalCopy(a, b)

	if !a.closed {
		t.Fatal("a was not closed")
	}
	if !b.closed {
		t.Fatal("b was not closed")
	}
}

func TestBidirectionalCopy_EmptyStreams(t *testing.T) {
	a := &closerPair{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}}
	b := &closerPair{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}}

	bidirectionalCopy(a, b)

	if !a.closed {
		t.Fatal("a was not closed")
	}
	if !b.closed {
		t.Fatal("b was not closed")
	}
}

func TestBidirectionalCopy_LargeData(t *testing.T) {
	dataA := strings.Repeat("x", 128*1024)
	dataB := strings.Repeat("y", 64*1024)

	aReader := strings.NewReader(dataA)
	aWriter := &bytes.Buffer{}
	bReader := strings.NewReader(dataB)
	bWriter := &bytes.Buffer{}

	a := struct {
		io.Reader
		io.Writer
	}{aReader, aWriter}
	b := struct {
		io.Reader
		io.Writer
	}{bReader, bWriter}

	bidirectionalCopy(a, b)

	if !strings.Contains(bWriter.String(), dataA) {
		t.Fatalf("b did not receive all data from a (got %d bytes)", bWriter.Len())
	}
	if !strings.Contains(aWriter.String(), dataB) {
		t.Fatalf("a did not receive all data from b (got %d bytes)", aWriter.Len())
	}
}

type noCloser struct {
	io.Reader
	io.Writer
}

func TestBidirectionalCopy_NoCloserInterface(t *testing.T) {
	a := &noCloser{Reader: strings.NewReader("test"), Writer: &bytes.Buffer{}}
	b := &noCloser{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}}

	bidirectionalCopy(a, b)

	if !strings.Contains(b.Writer.(*bytes.Buffer).String(), "test") {
		t.Fatalf("b did not receive data: %q", b.Writer.(*bytes.Buffer).String())
	}
}

type errReadWriter struct {
	readErr  error
	writeErr error
}

func (rw *errReadWriter) Read(p []byte) (int, error) {
	if rw.readErr != nil {
		return 0, rw.readErr
	}
	return 0, io.EOF
}

func (rw *errReadWriter) Write(p []byte) (int, error) {
	if rw.writeErr != nil {
		return 0, rw.writeErr
	}
	return len(p), nil
}

func (rw *errReadWriter) Close() error { return nil }

func TestBidirectionalCopy_ReadError(t *testing.T) {
	a := &errReadWriter{readErr: io.ErrUnexpectedEOF}
	b := &closerPair{Reader: strings.NewReader(""), Writer: &bytes.Buffer{}}

	bidirectionalCopy(a, b)

	if !b.closed {
		t.Fatal("b was not closed after read error")
	}
}

func TestBidirectionalCopy_WriteError(t *testing.T) {
	a := &errReadWriter{readErr: io.ErrUnexpectedEOF}
	b := &errReadWriter{writeErr: io.ErrShortWrite}

	bidirectionalCopy(a, b)
}

func TestIsTimeoutError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline exceeded", os.ErrDeadlineExceeded, true},
		{"op error wrapping deadline", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, true},
		{"closed", net.ErrClosed, false},
		{"unexpected eof", io.ErrUnexpectedEOF, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTimeoutError(tc.err); got != tc.want {
				t.Errorf("isTimeoutError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestCopyErrorKind(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"graceful remote close", &quic.ApplicationError{Remote: true, ErrorCode: 0}, ""},
		{"wrapped graceful remote close", fmt.Errorf("read: %w", &quic.ApplicationError{Remote: true, ErrorCode: 0}), ""},
		{"application error", &quic.ApplicationError{Remote: true, ErrorCode: 1}, "disconnect"},
		{"idle timeout", &quic.IdleTimeoutError{}, "disconnect"},
		{"stateless reset", &quic.StatelessResetError{}, "disconnect"},
		{"transport error", &quic.TransportError{}, "disconnect"},
		{"local closed", net.ErrClosed, ""},
		{"closed pipe", io.ErrClosedPipe, ""},
		{"deadline exceeded", os.ErrDeadlineExceeded, "timeout"},
		{"op error wrapping deadline", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}, "timeout"},
		{"unexpected eof", io.ErrUnexpectedEOF, "error"},
		{"plain error", errors.New("boom"), "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := copyErrorKind(tc.err); got != tc.want {
				t.Errorf("copyErrorKind(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestStartStdinPipe_ContextCancel(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer reader.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		StartStdinPipe(ctx, struct {
			io.Reader
			io.Writer
			io.Closer
		}{reader, io.Discard, writer})
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartStdinPipe did not exit after context cancel")
	}
}
