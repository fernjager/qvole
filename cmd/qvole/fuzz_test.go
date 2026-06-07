package main

import (
	"crypto/rand"
	"net"
	"strings"
	"testing"

	"github.com/fernjager/qvole/internal/app"
	"github.com/fernjager/qvole/internal/util"
	"github.com/fernjager/qvole/relay"
	"github.com/fernjager/qvole/spake2"
)

func FuzzNameplate(f *testing.F) {
	seeds := []string{"9908-ability-subway-unicorn", "hello", "", "a", "1234-test", "ABCD-EFGH", "a-b-c-d"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, code string) {
		np := util.Nameplate(code)
		if len(np) == 0 {
			t.Errorf("Nameplate(%q) returned empty string", code)
		}
	})
}

func FuzzSplitTunnelRequest(f *testing.F) {
	seeds := []string{"8080:localhost:80", "[::1]:8080:localhost:80", "127.0.0.1:8080:[::1]:80", "a:b:c", ":::", "[::1", "]abc[", "[", "[[::]]:80:80"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, spec string) {
		parts := app.SplitTunnelRequest(spec)
		if parts != nil && len(parts) == 0 {
			t.Errorf("SplitTunnelRequest(%q) returned empty non-nil slice", spec)
		}
		// Any 3-4 part split should be a parseable tunnel spec.
		if parts != nil && (len(parts) == 3 || len(parts) == 4) {
			if _, err := app.ParseTunnelRequest(spec, "L"); err != nil {
				t.Errorf("ParseTunnelRequest(%q) = %v, parts=%v", spec, err, parts)
			}
		}
	})
}

func FuzzPasswordToScalar(f *testing.F) {
	seeds := []string{"", "short", "a-reasonable-length-password", "x", strings.Repeat("x", 256), "\x00", "\x00\x00\x00", "\xff", "héllo", "日本語"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, password string) {
		s := spake2.PasswordToScalar(password)
		if s == nil {
			t.Error("passwordToScalar returned nil")
		}
		if s.Sign() == 0 {
			t.Error("passwordToScalar returned zero scalar")
		}
		if s.Cmp(spake2.Curve.Params().N) >= 0 {
			t.Error("passwordToScalar returned scalar >= curve order")
		}
	})
}

func FuzzEncryptDecrypt(f *testing.F) {
	seeds := []string{"192.168.1.1:9000", "10.0.0.1:12345", "[::1]:8080", ""}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, addr string) {
		key := make([]byte, 32)
		_, err := rand.Read(key)
		if err != nil {
			t.Skip("rand.Read failed")
		}

		aad := []byte("fuzz-aad")

		ciphertext, err := spake2.EncryptMetadata(key, aad, []byte(addr))
		if err != nil {
			t.Fatalf("encryptMetadata(%q): %v", addr, err)
		}

		plaintext, err := spake2.DecryptMetadata(key, aad, ciphertext)
		if err != nil {
			t.Fatalf("decryptMetadata(%q): %v", addr, err)
		}

		if string(plaintext) != addr {
			t.Fatalf("roundtrip mismatch: got %q, want %q", plaintext, addr)
		}
	})
}

func FuzzGenerateCode(f *testing.F) {
	seeds := []string{"1234-ability-subway-unicorn", "0000-aaaa-bbbb-cccc", "", "abc", "12345-too-long", "abcd-"}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, code string) {
		parts := strings.SplitN(code, "-", 2)
		np := util.Nameplate(code)
		if !util.GeneratedCodeRe.MatchString(code) {
			if len(np) != 8 {
				t.Errorf("Nameplate(%q) = %q, want 8 hex chars", code, np)
			}
		} else {
			if np != parts[0] {
				t.Errorf("Nameplate(%q) = %q, want prefix %q", code, np, parts[0])
			}
		}
	})
}

func FuzzHandlePacket(f *testing.F) {
	seeds := []string{
		"MSGD spake2 " + strings.Repeat("00", 97),
		"MSGD confirm " + strings.Repeat("00", 128),
		"MSGD spake2 " + strings.Repeat("ff", 50),
		"MSGD confirm " + strings.Repeat("aa", 64),
		"REGD testroom 127.0.0.1:12345\n",
		"MSGD unknown payload\n",
		"",
		"MSGD spake2 zz",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, relayResponse string) {
		relayConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Skip("cannot listen")
		}
		defer relayConn.Close()

		udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			t.Skip("cannot listen")
		}
		defer udpConn.Close()

		srcAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
		relay.HandlePacket(relayConn, []byte(relayResponse), srcAddr)
	})
}

func FuzzDecryptMetadata(f *testing.F) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	aad := []byte("fuzz-corrupt-aad")
	validCT, err := spake2.EncryptMetadata(key, aad, []byte("127.0.0.1:9000"))
	if err != nil {
		f.Fatal(err)
	}

	f.Add(validCT)
	f.Add([]byte{})
	f.Add(make([]byte, 1))
	f.Add(make([]byte, 12))
	f.Add(validCT[:len(validCT)-1])

	f.Fuzz(func(t *testing.T, ciphertext []byte) {
		plaintext, err := spake2.DecryptMetadata(key, aad, ciphertext)
		if err == nil && plaintext == nil {
			t.Error("DecryptMetadata returned nil plaintext without error")
		}
	})
}

func FuzzComputeShared(f *testing.F) {
	peerState, _ := spake2.NewState("fuzz-shared")
	validPoint := peerState.BlindedBytesM()
	peerState.Destroy()

	f.Add(validPoint)
	f.Add([]byte{})
	f.Add(make([]byte, 1))
	f.Add(make([]byte, 65))
	f.Add(make([]byte, 33))

	f.Fuzz(func(t *testing.T, peerPointBytes []byte) {
		state, err := spake2.NewState("fuzz-shared")
		if err != nil {
			t.Skip("failed to create SPAKE2 state")
		}
		defer state.Destroy()

		shared, err := state.ComputeShared(peerPointBytes, true)
		if err == nil && shared == nil {
			t.Error("ComputeShared returned nil shared without error")
		}
		spake2.ZeroBytes(shared)
	})
}

func FuzzVerifyConfirm(f *testing.F) {
	myState, _ := spake2.NewState("fuzz-verify")
	peerState, _ := spake2.NewState("fuzz-verify")
	myM := myState.BlindedBytesM()
	peerM := peerState.BlindedBytesM()
	myN := myState.BlindedBytesN()
	peerN := peerState.BlindedBytesN()

	myIsServer := string(myM) > string(peerM)
	var myPoint, peerPoint []byte
	var peerUsedM bool
	if myIsServer {
		myPoint = myN
		peerPoint = peerM
		peerUsedM = true
	} else {
		myPoint = myM
		peerPoint = peerN
		peerUsedM = false
	}

	myShared, _ := myState.ComputeShared(peerPoint, peerUsedM)
	ck, _, _ := spake2.DeriveSessionKey(myShared, myPoint, peerPoint)

	validNonce := make([]byte, 16)
	_, _ = rand.Read(validNonce)
	validFingerprint := make([]byte, 32)
	validConfirm, _ := spake2.ComputeConfirm(ck, myPoint, peerPoint, validNonce, validFingerprint)
	wrongConfirm := make([]byte, 32)

	f.Add(validConfirm, validNonce)
	f.Add(wrongConfirm, validNonce)
	f.Add(validConfirm, make([]byte, 16))
	f.Add([]byte{}, []byte{})
	f.Add(make([]byte, 32), []byte{0x01})

	f.Fuzz(func(t *testing.T, confirm, nonce []byte) {
		if len(nonce) == 0 {
			return
		}
		spake2.VerifyConfirm(ck, myPoint, peerPoint, nonce, validFingerprint, confirm)
	})
}
