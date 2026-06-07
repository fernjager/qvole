package tests

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/fernjager/qvole"
)

func TestLibraryDialAccept_Basic(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9900-lib-dial-basic"
	payload := []byte("hello-library-test\n")
	ctx := context.Background()

	type result struct {
		data []byte
		err  error
	}
	acceptorCh := make(chan result, 1)

	go func() {
		conn, err := qvole.Accept(ctx,
			qvole.WithCode(code),
			qvole.WithRelay(relayAddr),
		)
		if err != nil {
			acceptorCh <- result{err: err}
			return
		}
		defer conn.Close()
		var buf [4096]byte
		n, err := conn.Read(buf[:])
		if err != nil && err != io.EOF {
			acceptorCh <- result{err: err}
			return
		}
		acceptorCh <- result{data: buf[:n]}
	}()

	time.Sleep(500 * time.Millisecond)

	dialConn, err := qvole.Dial(ctx,
		qvole.WithCode(code),
		qvole.WithRelay(relayAddr),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer dialConn.Close()
	if _, err := dialConn.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	qvole.CloseWrite(dialConn)

	select {
	case r := <-acceptorCh:
		if r.err != nil {
			t.Fatalf("Accept: %v", r.err)
		}
		if !bytes.HasPrefix(r.data, payload[:len(payload)-1]) {
			t.Errorf("expected %q, got %q", payload, r.data)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout waiting for acceptor")
	}
}

func TestLibraryDialAccept_LargePayload(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := "9901-lib-dial-large"
	payloadSize := 65536
	payload := make([]byte, payloadSize)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	ctx := context.Background()

	acceptorDone := make(chan error, 1)
	var received []byte

	go func() {
		conn, err := qvole.Accept(ctx,
			qvole.WithCode(code),
			qvole.WithRelay(relayAddr),
		)
		if err != nil {
			acceptorDone <- err
			return
		}
		defer conn.Close()
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				received = append(received, buf[:n]...)
			}
			if err != nil {
				if err == io.EOF {
					acceptorDone <- nil
				} else {
					acceptorDone <- err
				}
				return
			}
		}
	}()

	time.Sleep(500 * time.Millisecond)

	dialConn, err := qvole.Dial(ctx,
		qvole.WithCode(code),
		qvole.WithRelay(relayAddr),
	)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer dialConn.Close()
	if _, err := dialConn.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	qvole.CloseWrite(dialConn)

	select {
	case err := <-acceptorDone:
		if err != nil {
			t.Fatalf("Accept: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout waiting for acceptor")
	}

	if len(received) < payloadSize {
		t.Errorf("received %d bytes, expected >= %d", len(received), payloadSize)
	} else if !bytes.Equal(received[:payloadSize], payload) {
		t.Error("payload mismatch")
	}
}

func TestLibraryDialAccept_WrongCode(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go func() {
		qvole.Accept(ctx,
			qvole.WithCode("AAAA-aaaa-bbbb-cccc"),
			qvole.WithRelay(relayAddr),
		)
	}()

	time.Sleep(200 * time.Millisecond)

	_, err := qvole.Dial(ctx,
		qvole.WithCode("BBBB-zzzz-yyyy-xxxx"),
		qvole.WithRelay(relayAddr),
		qvole.WithExchangeDeadline(3*time.Second),
	)
	if err == nil {
		t.Fatal("expected error for wrong code")
	}
}

func TestLibraryDial_EmptyCode(t *testing.T) {
	_, err := qvole.Dial(context.Background(),
		qvole.WithRelay("localhost:9009"),
	)
	if err == nil {
		t.Fatal("expected error for empty code")
	}
	if err.Error() != "code is required" {
		t.Errorf("expected 'code is required', got %q", err.Error())
	}
}

func TestLibraryDial_EmptyRelay(t *testing.T) {
	_, err := qvole.Dial(context.Background(),
		qvole.WithCode("test-code"),
	)
	if err == nil {
		t.Fatal("expected error for empty relay")
	}
	if err.Error() != "relay is required" {
		t.Errorf("expected 'relay is required', got %q", err.Error())
	}
}

func TestLibraryAccept_EmptyCode(t *testing.T) {
	_, err := qvole.Accept(context.Background(),
		qvole.WithRelay("localhost:9009"),
	)
	if err == nil {
		t.Fatal("expected error for empty code")
	}
}

func TestLibraryExec_Echo(t *testing.T) {
	relayAddr, cleanup := testRelay(t)
	defer cleanup()

	code := fmt.Sprintf("9902-lib-exec-%d", time.Now().UnixNano()%10000)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	execDone := make(chan error, 1)
	go func() {
		execDone <- qvole.Exec(ctx,
			qvole.WithCode(code),
			qvole.WithRelay(relayAddr),
			qvole.WithCmdMode(true),
			qvole.WithCommand("echo hello-lib-exec"),
		)
	}()

	time.Sleep(500 * time.Millisecond)

	// Peer side: accept and read output
	conn, err := qvole.Accept(ctx,
		qvole.WithCode(code),
		qvole.WithRelay(relayAddr),
	)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer conn.Close()

	var outBuf bytes.Buffer
	io.Copy(&outBuf, conn)

	err = <-execDone
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !bytes.Contains(outBuf.Bytes(), []byte("hello-lib-exec")) {
		t.Errorf("expected 'hello-lib-exec', got %q", outBuf.String())
	}
}
