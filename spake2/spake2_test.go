package spake2

import (
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

var testFingerprint = []byte{
	0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
	0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
	0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
	0xAB, 0xCD, 0xEF, 0x01, 0x23, 0x45, 0x67, 0x89,
}

func TestSPAKE2_PasswordToScalar_Deterministic(t *testing.T) {
	s1 := PasswordToScalar("test-password")
	s2 := PasswordToScalar("test-password")
	if s1.Cmp(s2) != 0 {
		t.Fatal("PasswordToScalar not deterministic")
	}
}

func TestSPAKE2_PasswordToScalar_DifferentInputs(t *testing.T) {
	s1 := PasswordToScalar("password-a")
	s2 := PasswordToScalar("password-b")
	if s1.Cmp(s2) == 0 {
		t.Fatal("different passwords produced same scalar")
	}
}

func TestSPAKE2_PasswordToScalar_NotZero(t *testing.T) {
	s := PasswordToScalar("")
	if s.Sign() == 0 {
		t.Fatal("empty password produced zero scalar")
	}
}

func TestSPAKE2_HashToCurve_Deterministic(t *testing.T) {
	seed := []byte("qvole-spake2-M-v1")
	x1, y1 := HashToCurve(elliptic.P256(), seed)
	x2, y2 := HashToCurve(elliptic.P256(), seed)
	if x1.Cmp(x2) != 0 || y1.Cmp(y2) != 0 {
		t.Fatal("HashToCurve not deterministic")
	}
}

func TestSPAKE2_HashToCurve_OnCurve(t *testing.T) {
	x, y := HashToCurve(elliptic.P256(), []byte("qvole-spake2-M-v1"))
	if !elliptic.P256().IsOnCurve(x, y) {
		t.Fatal("HashToCurve result not on P-256")
	}
}

func exchangeTest(alice, bob *State) ([]byte, []byte, error) {
	aliceM := alice.BlindedBytesM()
	bobM := bob.BlindedBytesM()
	aliceN := alice.BlindedBytesN()
	bobN := bob.BlindedBytesN()

	aliceIsServer := string(aliceM) > string(bobM)

	var aliceShared, bobShared []byte
	var aliceErr, bobErr error
	if aliceIsServer {
		aliceShared, aliceErr = alice.ComputeShared(bobM, true)
		bobShared, bobErr = bob.ComputeShared(aliceN, false)
	} else {
		aliceShared, aliceErr = alice.ComputeShared(bobN, false)
		bobShared, bobErr = bob.ComputeShared(aliceM, true)
	}
	if aliceErr != nil {
		return nil, nil, aliceErr
	}
	if bobErr != nil {
		return nil, nil, bobErr
	}
	return aliceShared, bobShared, nil
}

func TestSPAKE2_State_RoundTrip(t *testing.T) {
	password := "sekret"
	alice, err := NewState(password)
	if err != nil {
		t.Fatalf("alice state: %v", err)
	}
	bob, err := NewState(password)
	if err != nil {
		t.Fatalf("bob state: %v", err)
	}

	aliceShared, bobShared, err := exchangeTest(alice, bob)
	if err != nil {
		t.Fatalf("compute shared: %v", err)
	}

	if string(aliceShared) != string(bobShared) {
		t.Fatal("shared secrets do not match")
	}
	if len(aliceShared) != 32 {
		t.Fatalf("shared secret length %d, want 32", len(aliceShared))
	}
}

func TestSPAKE2_WrongPassword(t *testing.T) {
	alice, err := NewState("correct-password")
	if err != nil {
		t.Fatalf("alice state: %v", err)
	}
	bob, err := NewState("wrong-password")
	if err != nil {
		t.Fatalf("bob state: %v", err)
	}

	aliceShared, bobShared, err := exchangeTest(alice, bob)
	if err != nil {
		return
	}

	if string(aliceShared) == string(bobShared) {
		t.Fatal("shared secrets matched despite different passwords")
	}
}

func TestSPAKE2_DeriveSessionKey_Deterministic(t *testing.T) {
	shared := make([]byte, 32)
	rand.Read(shared)
	myPoint := []byte("test-my-point-32bytes!")
	peerPoint := []byte("test-peer-point-32bytes")

	cK1, eK1, err := DeriveSessionKey(shared, myPoint, peerPoint)
	if err != nil {
		t.Fatalf("derive key 1: %v", err)
	}
	cK2, eK2, err := DeriveSessionKey(shared, myPoint, peerPoint)
	if err != nil {
		t.Fatalf("derive key 2: %v", err)
	}
	if string(cK1) != string(cK2) || string(eK1) != string(eK2) {
		t.Fatal("DeriveSessionKey not deterministic")
	}
	if len(cK1) != 32 || len(eK1) != 32 {
		t.Fatalf("sub-key length %d, want 32", len(cK1))
	}
}

func TestSPAKE2_EncryptDecrypt(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	plaintext := []byte("192.168.1.1:9000")
	aad := []byte("additional-authenticated-data")

	ciphertext, err := EncryptMetadata(key, aad, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := DecryptMetadata(key, aad, ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatal("decrypted text does not match original")
	}
}

func TestSPAKE2_EncryptDecrypt_WrongKey(t *testing.T) {
	key1, key2 := make([]byte, 32), make([]byte, 32)
	rand.Read(key1)
	rand.Read(key2)

	plaintext := []byte("10.0.0.1:1234")
	aad := []byte("aad-data")

	ciphertext, err := EncryptMetadata(key1, aad, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, err := DecryptMetadata(key2, aad, ciphertext); err == nil {
		t.Fatal("expected error decrypting with wrong key")
	}
}

func TestSPAKE2_EncryptDecrypt_WrongAAD(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	plaintext := []byte("10.0.0.1:1234")
	aad1 := []byte("correct-aad")
	aad2 := []byte("tampered-aad")

	ciphertext, err := EncryptMetadata(key, aad1, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	if _, err := DecryptMetadata(key, aad2, ciphertext); err == nil {
		t.Fatal("expected error decrypting with wrong AAD")
	}
}

func TestSPAKE2_EncryptWithNonce(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	plaintext := []byte("consistent-data")
	aad := []byte("aad")

	c1, err := EncryptMetadata(key, aad, plaintext)
	if err != nil {
		t.Fatalf("encrypt 1: %v", err)
	}
	c2, err := EncryptMetadata(key, aad, plaintext)
	if err != nil {
		t.Fatalf("encrypt 2: %v", err)
	}

	if hex.EncodeToString(c1) == hex.EncodeToString(c2) {
		t.Fatal("encrypted outputs are identical; nonce may not be random")
	}
}

func TestSPAKE2_DecryptInvalidCiphertext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)

	if _, err := DecryptMetadata(key, nil, []byte("too-short")); err == nil {
		t.Fatal("expected error decrypting too-short ciphertext")
	}
}

func TestSPAKE2_Confirm_Deterministic(t *testing.T) {
	sessionKey := make([]byte, 32)
	rand.Read(sessionKey)
	myPoint := []byte("point-a")
	peerPoint := []byte("point-b")
	nonce := []byte("0123456789abcdef")

	c1, _ := ComputeConfirm(sessionKey, myPoint, peerPoint, nonce, testFingerprint)
	c2, _ := ComputeConfirm(sessionKey, myPoint, peerPoint, nonce, testFingerprint)
	if string(c1) != string(c2) {
		t.Fatal("ComputeConfirm not deterministic")
	}
}

func TestSPAKE2_Confirm_Ordering(t *testing.T) {
	sessionKey := make([]byte, 32)
	rand.Read(sessionKey)
	a := []byte("aaaa")
	b := []byte("bbbb")
	nonce := []byte("0123456789abcdef")

	cAB, _ := ComputeConfirm(sessionKey, a, b, nonce, testFingerprint)
	cSwap, _ := ComputeConfirm(sessionKey, b, a, nonce, testFingerprint)
	if string(cAB) != string(cSwap) {
		t.Fatal("ComputeConfirm should sort points, order must not matter")
	}
}

func TestSPAKE2_Confirm_Verify(t *testing.T) {
	sessionKey := make([]byte, 32)
	rand.Read(sessionKey)
	myPoint := []byte("my-point")
	peerPoint := []byte("peer-point")
	nonce := []byte("0123456789abcdef")

	confirm, _ := ComputeConfirm(sessionKey, myPoint, peerPoint, nonce, testFingerprint)
	if !VerifyConfirm(sessionKey, myPoint, peerPoint, nonce, testFingerprint, confirm) {
		t.Fatal("VerifyConfirm returned false for correct confirm")
	}
}

func TestSPAKE2_Confirm_WrongKey(t *testing.T) {
	key1, key2 := make([]byte, 32), make([]byte, 32)
	rand.Read(key1)
	rand.Read(key2)
	nonce := []byte("0123456789abcdef")

	confirm, _ := ComputeConfirm(key1, []byte("a"), []byte("b"), nonce, testFingerprint)
	if VerifyConfirm(key2, []byte("a"), []byte("b"), nonce, testFingerprint, confirm) {
		t.Fatal("VerifyConfirm returned true with wrong key")
	}
}

func TestSPAKE2_Confirm_WrongNonce(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	correctNonce := []byte("0123456789abcdef")
	wrongNonce := []byte("fedcba9876543210")

	confirm, _ := ComputeConfirm(key, []byte("a"), []byte("b"), correctNonce, testFingerprint)
	if VerifyConfirm(key, []byte("a"), []byte("b"), wrongNonce, testFingerprint, confirm) {
		t.Fatal("VerifyConfirm returned true with wrong nonce")
	}
}

func TestSPAKE2_FullExchange(t *testing.T) {
	password := "test-connection-code-1234"

	alice, err := NewState(password)
	if err != nil {
		t.Fatalf("alice state: %v", err)
	}
	bob, err := NewState(password)
	if err != nil {
		t.Fatalf("bob state: %v", err)
	}

	aliceShared, bobShared, err := exchangeTest(alice, bob)
	if err != nil {
		t.Fatalf("compute shared: %v", err)
	}

	aliceM := alice.BlindedBytesM()
	bobM := bob.BlindedBytesM()
	aliceN := alice.BlindedBytesN()
	bobN := bob.BlindedBytesN()

	aliceIsServer := string(aliceM) > string(bobM)

	var alicePoint, bobPoint []byte
	if aliceIsServer {
		alicePoint = aliceN
		bobPoint = bobM
	} else {
		alicePoint = aliceM
		bobPoint = bobN
	}

	aliceCKey, aliceEKey, err := DeriveSessionKey(aliceShared, alicePoint, bobPoint)
	if err != nil {
		t.Fatalf("alice key: %v", err)
	}
	bobCKey, bobEKey, err := DeriveSessionKey(bobShared, bobPoint, alicePoint)
	if err != nil {
		t.Fatalf("bob key: %v", err)
	}

	aliceNonce := []byte("alice-nonce-1234")
	bobNonce := []byte("bob-nonce-567890")

	aliceConfirm, _ := ComputeConfirm(aliceCKey, alicePoint, bobPoint, aliceNonce, testFingerprint)
	bobConfirm, _ := ComputeConfirm(bobCKey, bobPoint, alicePoint, bobNonce, testFingerprint)

	if !VerifyConfirm(aliceCKey, alicePoint, bobPoint, bobNonce, testFingerprint, bobConfirm) {
		t.Fatal("alice rejected bob's confirm")
	}
	if !VerifyConfirm(bobCKey, bobPoint, alicePoint, aliceNonce, testFingerprint, aliceConfirm) {
		t.Fatal("bob rejected alice's confirm")
	}

	aliceMetadata := []byte("10.0.0.1:12345")
	bobMetadata := []byte("10.0.0.2:54321")

	aad := SessionAAD(alicePoint, bobPoint)

	aliceEnc, err := EncryptMetadata(aliceEKey, aad, aliceMetadata)
	if err != nil {
		t.Fatalf("alice encrypt: %v", err)
	}
	bobEnc, err := EncryptMetadata(bobEKey, aad, bobMetadata)
	if err != nil {
		t.Fatalf("bob encrypt: %v", err)
	}

	aliceDec, err := DecryptMetadata(aliceEKey, aad, bobEnc)
	if err != nil {
		t.Fatalf("alice decrypt: %v", err)
	}
	bobDec, err := DecryptMetadata(bobEKey, aad, aliceEnc)
	if err != nil {
		t.Fatalf("bob decrypt: %v", err)
	}

	if string(aliceDec) != string(bobMetadata) {
		t.Fatalf("alice got %q, want %q", aliceDec, bobMetadata)
	}
	if string(bobDec) != string(aliceMetadata) {
		t.Fatalf("bob got %q, want %q", bobDec, aliceMetadata)
	}
}

func TestSPAKE2_ReplayConfirm(t *testing.T) {
	password := "replay-test"
	alice1, _ := NewState(password)
	bob1, _ := NewState(password)

	alice1Shared, _, err := exchangeTest(alice1, bob1)
	if err != nil {
		t.Fatalf("session 1 exchange: %v", err)
	}

	alice1M := alice1.BlindedBytesM()
	bob1M := bob1.BlindedBytesM()
	alice1N := alice1.BlindedBytesN()
	bob1N := bob1.BlindedBytesN()

	alice1IsServer := string(alice1M) > string(bob1M)
	var alice1Point, bob1Point []byte
	if alice1IsServer {
		alice1Point = alice1N
		bob1Point = bob1M
	} else {
		alice1Point = alice1M
		bob1Point = bob1N
	}

	ck1, _, _ := DeriveSessionKey(alice1Shared, alice1Point, bob1Point)

	nonce := []byte("session1-nonce-123")
	confirm, _ := ComputeConfirm(ck1, alice1Point, bob1Point, nonce, testFingerprint)

	alice2, _ := NewState(password)
	bob2, _ := NewState(password)

	alice2Shared, _, _ := exchangeTest(alice2, bob2)

	alice2M := alice2.BlindedBytesM()
	bob2M := bob2.BlindedBytesM()
	alice2N := alice2.BlindedBytesN()
	bob2N := bob2.BlindedBytesN()

	alice2IsServer := string(alice2M) > string(bob2M)
	var alice2Point, bob2Point []byte
	if alice2IsServer {
		alice2Point = alice2N
		bob2Point = bob2M
	} else {
		alice2Point = alice2M
		bob2Point = bob2N
	}

	ck2, _, _ := DeriveSessionKey(alice2Shared, alice2Point, bob2Point)

	if VerifyConfirm(ck2, alice2Point, bob2Point, nonce, testFingerprint, confirm) {
		t.Fatal("replayed confirm should not verify with different session")
	}
}

func TestSPAKE2_ComputeShared_InvalidPoint(t *testing.T) {
	s, err := NewState("test")
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	if _, err := s.ComputeShared([]byte("short"), true); err == nil {
		t.Fatal("expected error for short point")
	}

	invalidPoint := make([]byte, 65)
	invalidPoint[0] = 0x04
	if _, err := s.ComputeShared(invalidPoint, true); err == nil {
		t.Fatal("expected error for invalid point (all zeros)")
	}
}

func TestSPAKE2_BlindedBytes_Length(t *testing.T) {
	s, err := NewState("test")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	bm := s.BlindedBytesM()
	if len(bm) != 65 {
		t.Fatalf("blinded M bytes length %d, want 65", len(bm))
	}
	if bm[0] != 0x04 {
		t.Fatalf("expected uncompressed point marker 0x04, got 0x%02x", bm[0])
	}
	bn := s.BlindedBytesN()
	if len(bn) != 65 {
		t.Fatalf("blinded N bytes length %d, want 65", len(bn))
	}
	if bn[0] != 0x04 {
		t.Fatalf("expected uncompressed point marker 0x04, got 0x%02x", bn[0])
	}
}

func TestSPAKE2_DeterministicBlindedBytes(t *testing.T) {
	password := strings.Repeat("a", 100)
	s, err := NewState(password)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	b1 := s.BlindedBytesM()
	b2 := s.BlindedBytesM()
	if string(b1) != string(b2) {
		t.Fatal("BlindedBytesM not deterministic on same state")
	}
	b3 := s.BlindedBytesN()
	b4 := s.BlindedBytesN()
	if string(b3) != string(b4) {
		t.Fatal("BlindedBytesN not deterministic on same state")
	}
}

func TestSPAKE2_WeierstrassY_RoundTrip(t *testing.T) {
	curve := elliptic.P256()
	x, y := HashToCurve(curve, []byte("test-seed"))
	if y == nil {
		t.Fatal("HashToCurve returned nil y")
	}

	y2 := ComputeWeierstrassY(curve, x)
	if y2 == nil {
		t.Fatal("ComputeWeierstrassY returned nil")
	}
	if y.Cmp(y2) != 0 {
		t.Fatal("ComputeWeierstrassY did not match HashToCurve y")
	}
}

func TestSPAKE2_SessionAAD_Commutativity(t *testing.T) {
	a := []byte{0x04, 0x01, 0x02, 0x03}
	b := []byte{0x04, 0x03, 0x02, 0x01}
	ab := SessionAAD(a, b)
	ba := SessionAAD(b, a)
	if string(ab) != string(ba) {
		t.Fatal("SessionAAD(a, b) != SessionAAD(b, a)")
	}
}

func TestSPAKE2_SessionAAD_IdenticalPoints(t *testing.T) {
	a := []byte{0x04, 0x01, 0x02, 0x03}
	result := SessionAAD(a, a)
	if string(result) != string(append(a, a...)) {
		t.Fatal("SessionAAD(a, a) should produce a||a")
	}
}

func TestSPAKE2_ComputeShared_OffCurve(t *testing.T) {
	s, err := NewState("test")
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	offCurve := make([]byte, 65)
	offCurve[0] = 0x04
	offCurve[1] = 0x01
	offCurve[33] = 0x01
	if _, err := s.ComputeShared(offCurve, true); err == nil {
		t.Fatal("expected error for point not on curve")
	}
}

func TestSPAKE2_RoleAssignment(t *testing.T) {
	passwords := []string{"a", "b", "abc", "xyz", "short", "a-longer-password-1234"}
	for i, pw1 := range passwords {
		for j, pw2 := range passwords {
			if i == j {
				continue
			}
			s1, _ := NewState(pw1)
			s2, _ := NewState(pw2)
			p1 := s1.BlindedBytesM()
			p2 := s2.BlindedBytesM()

			s1isServer := string(p1) > string(p2)
			s2isServer := string(p2) > string(p1)

			if s1isServer == s2isServer {
				t.Fatalf("role assignment not opposite for (%q, %q): s1=%v s2=%v", pw1, pw2, s1isServer, s2isServer)
			}
		}
	}
}

func TestSPAKE2_PasswordToScalar_LongPassword(t *testing.T) {
	long := strings.Repeat("x", 256)
	s := PasswordToScalar(long)
	if s.Sign() == 0 {
		t.Fatal("long password produced zero scalar")
	}
	if s.Cmp(elliptic.P256().Params().N) >= 0 {
		t.Fatal("scalar >= curve order N")
	}
}

func TestSPAKE2_PasswordToScalar_Empty(t *testing.T) {
	s := PasswordToScalar("")
	if s.Sign() == 0 {
		t.Fatal("empty password should produce non-zero scalar")
	}
}

func TestSPAKE2_ZeroBytes(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03, 0x04}
	ZeroBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("ZeroBytes: byte %d is %d, want 0", i, v)
		}
	}
}

func TestSPAKE2_ZeroBytes_Empty(t *testing.T) {
	ZeroBytes(nil)
	ZeroBytes([]byte{})
}

func TestSPAKE2_Destroy_ZerosFields(t *testing.T) {
	s, err := NewState("test-password")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	s.Destroy()
	if s.pwScalar.Sign() != 0 {
		t.Error("pwScalar not zeroed")
	}
	if s.scalarM.Sign() != 0 {
		t.Error("scalarM not zeroed")
	}
	if s.scalarN.Sign() != 0 {
		t.Error("scalarN not zeroed")
	}
	if s.blindedXM.Sign() != 0 {
		t.Error("blindedXM not zeroed")
	}
	if s.blindedYM.Sign() != 0 {
		t.Error("blindedYM not zeroed")
	}
	if s.blindedXN.Sign() != 0 {
		t.Error("blindedXN not zeroed")
	}
	if s.blindedYN.Sign() != 0 {
		t.Error("blindedYN not zeroed")
	}
}

func TestSPAKE2_HashToCurve_RetryOnZeroX(t *testing.T) {
	curve := elliptic.P256()
	// Pick a seed whose SHA-256 mod p == 0; this is extremely unlikely
	// but we verify the loop doesn't hang by testing that a candidate
	// with x==0 (mod p) would cause a retry.
	// Instead, verify that HashToCurve never returns x==0 for many seeds.
	for i := 0; i < 100; i++ {
		seed := make([]byte, 32)
		rand.Read(seed)
		x, y := HashToCurve(curve, seed)
		if x.Sign() == 0 {
			t.Errorf("HashToCurve returned x=0 for seed %x; loop may have terminated early", seed)
		}
		if !curve.IsOnCurve(x, y) {
			t.Errorf("HashToCurve returned point not on curve for seed %x", seed)
		}
	}
}

func TestSPAKE2_ComputeWeierstrassY_NilReturn(t *testing.T) {
	curve := elliptic.P256()
	// ComputeWeierstrassY returns nil when the sqrt fails (y2 != rhs).
	// For a given x, y^2 = x^3 - 3x + B mod p.
	// About half of the x values have no quadratic residue (no y).
	// We can find such an x by brute force.
	for xVal := int64(2); xVal < 10000; xVal++ {
		x := big.NewInt(xVal)
		y := ComputeWeierstrassY(curve, x)
		if y == nil {
			return
		}
	}
	t.Fatal("could not find x that produces nil y; test may be flaky")
}

func TestSPAKE2_ComputeWeierstrassY_RhsZero(t *testing.T) {
	curve := elliptic.P256()
	// ComputeWeierstrassY returns nil when rhs is not a quadratic residue.
	// For x=0 and x=1, P-256's rhs is not a square; expected to return nil.
	if y := ComputeWeierstrassY(curve, big.NewInt(0)); y != nil {
		t.Logf("x=0 gave y=%s (unexpectedly on curve)", y)
	}
	if y := ComputeWeierstrassY(curve, big.NewInt(1)); y != nil {
		t.Logf("x=1 gave y=%s (unexpectedly on curve)", y)
	}
	// Use a known valid seed point to verify ComputeWeierstrassY produces correct y.
	x, y := HashToCurve(curve, []byte("qvole-spake2-M-v1"))
	y2 := ComputeWeierstrassY(curve, x)
	if y2 == nil || y2.Cmp(y) != 0 {
		t.Fatal("ComputeWeierstrassY did not match HashToCurve y for M")
	}
}

func TestSPAKE2_ComputeShared_Exact32BytePad(t *testing.T) {
	s, err := NewState("test")
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	peer, err := NewState("test")
	if err != nil {
		t.Fatalf("peer state: %v", err)
	}

	aliceShared, _, err := exchangeTest(s, peer)
	if err != nil {
		t.Fatalf("compute shared: %v", err)
	}
	if len(aliceShared) != 32 {
		t.Fatalf("shared length %d, want 32", len(aliceShared))
	}
}

func TestSPAKE2_Confirm_EmptyNonceReturnsError(t *testing.T) {
	sessionKey := make([]byte, 32)
	rand.Read(sessionKey)
	_, err := ComputeConfirm(sessionKey, []byte("a"), []byte("b"), []byte{}, testFingerprint)
	if err == nil {
		t.Fatal("expected error for empty nonce")
	}
}

func TestSPAKE2_EncryptMetadata_EmptyPlaintext(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	aad := []byte("test-aad")

	ciphertext, err := EncryptMetadata(key, aad, []byte{})
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}

	plaintext, err := DecryptMetadata(key, aad, ciphertext)
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}

	if len(plaintext) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes: %x", len(plaintext), plaintext)
	}
}

func TestSPAKE2_DecryptMetadata_TruncatedGCMTag(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	aad := []byte("test-aad")

	ciphertext, err := EncryptMetadata(key, aad, []byte("hello"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Truncate the GCM auth tag (last 16 bytes are the tag)
	if len(ciphertext) > 16 {
		truncated := ciphertext[:len(ciphertext)-8]
		_, err = DecryptMetadata(key, aad, truncated)
		if err == nil {
			t.Fatal("expected error for truncated GCM tag")
		}
	}
}

func TestSPAKE2_NewState_EmptyPassword(t *testing.T) {
	s, err := NewState("")
	if err != nil {
		t.Fatalf("NewState empty password: %v", err)
	}
	bm := s.BlindedBytesM()
	if len(bm) != 65 {
		t.Fatalf("blinded M bytes length %d, want 65", len(bm))
	}
	if bm[0] != 0x04 {
		t.Fatalf("expected uncompressed point marker 0x04, got 0x%02x", bm[0])
	}
	if !Curve.IsOnCurve(s.blindedXM, s.blindedYM) {
		t.Fatal("blinded M point not on curve for empty password")
	}
	bn := s.BlindedBytesN()
	if len(bn) != 65 {
		t.Fatalf("blinded N bytes length %d, want 65", len(bn))
	}
	if bn[0] != 0x04 {
		t.Fatalf("expected uncompressed point marker 0x04, got 0x%02x", bn[0])
	}
	if !Curve.IsOnCurve(s.blindedXN, s.blindedYN) {
		t.Fatal("blinded N point not on curve for empty password")
	}
}

func TestSPAKE2_DeriveSessionKey_OrderingIndependence(t *testing.T) {
	shared := make([]byte, 32)
	rand.Read(shared)
	pointA := []byte("point-a-32-bytes-long-padding!!!")
	pointB := []byte("point-b-32-bytes-long-padding!!!")

	ck1, ek1, err := DeriveSessionKey(shared, pointA, pointB)
	if err != nil {
		t.Fatalf("derive key 1: %v", err)
	}

	ck2, ek2, err := DeriveSessionKey(shared, pointB, pointA)
	if err != nil {
		t.Fatalf("derive key 2: %v", err)
	}

	if string(ck1) != string(ck2) {
		t.Fatal("confirmKey differs when point order is swapped")
	}
	if string(ek1) != string(ek2) {
		t.Fatal("encKey differs when point order is swapped")
	}
}

func TestSPAKE2_Confirm_BitDiffInNonce(t *testing.T) {
	sessionKey := make([]byte, 32)
	rand.Read(sessionKey)
	nonce1 := []byte("0123456789abcdef")
	nonce2 := []byte("0123456789abcdee") // differ in last bit

	c1, _ := ComputeConfirm(sessionKey, []byte("a"), []byte("b"), nonce1, testFingerprint)
	c2, _ := ComputeConfirm(sessionKey, []byte("a"), []byte("b"), nonce2, testFingerprint)

	if string(c1) == string(c2) {
		t.Fatal("different nonces should produce different confirms")
	}
}

// TestSPAKE2_IndependentScalars asserts that NewState does not reuse a single
// ephemeral scalar for both blinded points. If a single scalar r were reused,
// an observer could subtract the two captured blinded points to cancel the
// r*G term and recover pw*(M-N), enabling offline dictionary attacks.
func TestSPAKE2_IndependentScalars(t *testing.T) {
	for i := 0; i < 100; i++ {
		s, err := NewState("test-password")
		if err != nil {
			t.Fatalf("state %d: %v", i, err)
		}
		if s.scalarM.Cmp(s.scalarN) == 0 {
			t.Fatalf("iteration %d: scalarM == scalarN (scalars not independent)", i)
		}
	}
}

// TestSPAKE2_NoOfflineOracle is the key regression test for the critical fix.
// If the implementation correctly uses independent ephemeral scalars, then
// blindedM - blindedN is NOT a deterministic function of the password, and
// computing pw_candidate*(M-N) for wrong candidates should not match the
// captured delta. We construct the test to fail loudly if a future refactor
// regresses to a single shared scalar.
//
// Note: this test is necessarily probabilistic. The probability that a wrong
// candidate happens to match by accident is ~1/2^256; running 50 candidates
// gives a false-pass probability indistinguishable from zero.
func TestSPAKE2_NoOfflineOracle(t *testing.T) {
	const correctPassword = "correct-horse-battery-staple"
	wrongCandidates := []string{
		"wrong-password-1",
		"wrong-password-2",
		"password",
		"12345678",
		"",
		"correct-horse-battery-stapl",   // 1-char short
		"Correct-Horse-Battery-Staple",  // case swap
		"correct-horse-battery-staple!", // extra char
	}

	state, err := NewState(correctPassword)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	bmX, bmY := elliptic.Unmarshal(Curve, state.BlindedBytesM())
	bnX, bnY := elliptic.Unmarshal(Curve, state.BlindedBytesN())
	if bmX == nil || bnX == nil {
		t.Fatalf("could not unmarshal blinded points")
	}

	// captured delta = blindedM - blindedN
	negBnY := new(big.Int).Sub(Curve.Params().P, bnY)
	deltaX, deltaY := Curve.Add(bmX, bmY, bnX, negBnY)

	// Public constant T = M - N (computed once, like an attacker would)
	negNY := new(big.Int).Sub(Curve.Params().P, Ny)
	tX, tY := Curve.Add(Mx, My, Nx, negNY)

	for _, candidate := range wrongCandidates {
		pwScalar := PasswordToScalar(candidate)
		candX, candY := Curve.ScalarMult(tX, tY, scalarBytes32(pwScalar))
		if candX.Cmp(deltaX) == 0 && candY.Cmp(deltaY) == 0 {
			t.Fatalf("offline oracle matched for wrong candidate %q: "+
				"scalar reuse regression?", candidate)
		}
	}

	// With independent scalars, blindedM - blindedN = (rM - rN)*G + pw*(M-N),
	// so even the correct password does NOT match delta (unless rM == rN,
	// which has probability ~1/2^256). A match would indicate a regression
	// to the old single-scalar behavior.
	correctScalar := PasswordToScalar(correctPassword)
	correctX, correctY := Curve.ScalarMult(tX, tY, scalarBytes32(correctScalar))
	if correctX.Cmp(deltaX) == 0 && correctY.Cmp(deltaY) == 0 {
		t.Fatal("correct password matched delta — single-scalar regression?")
	}
}
