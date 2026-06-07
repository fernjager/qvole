package spake2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"os"
	"strconv"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

// We use crypto/elliptic instead of crypto/ecdh because SPAKE2 requires
// low-level point operations (addition, subtraction, arbitrary scalar
// multiplication) that crypto/ecdh's high-level API deliberately forbids.
// The P-256 ScalarMult is not fully constant-time; the ephemeral scalars
// used here are single-use per session so the risk is limited to local
// cache-timing attacks.

// Curve is the elliptic curve used for the SPAKE2 exchange (P-256).
var Curve = elliptic.P256()

// Mx, My are the hash-to-curve preimage of M, a fixed generator for the client side.
var Mx, My *big.Int

// Nx, Ny are the hash-to-curve preimage of N, a fixed generator for the server side.
var Nx, Ny *big.Int

var (
	kdfIterations int = 600000
)

func init() {
	Mx, My = HashToCurve(Curve, []byte("qvole-spake2-M-v1"))
	Nx, Ny = HashToCurve(Curve, []byte("qvole-spake2-N-v1"))
	if v := os.Getenv("QVOLE_KDF_ITERATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			kdfIterations = n
		}
	}
}

// HashToCurve maps a byte seed to a point on curve using hash-and-increment.
func HashToCurve(curve elliptic.Curve, seed []byte) (x, y *big.Int) {
	h := sha256.Sum256(seed)
	counter := uint64(0)
	p := curve.Params().P

	for {
		hasher := sha256.New()
		hasher.Write(h[:])
		binary.Write(hasher, binary.BigEndian, counter)
		candidate := hasher.Sum(nil)

		x = new(big.Int).SetBytes(candidate)
		x.Mod(x, p)

		if x.Sign() == 0 {
			counter++
			continue
		}

		y = ComputeWeierstrassY(curve, x)
		if y != nil && curve.IsOnCurve(x, y) {
			return x, y
		}
		counter++
	}
}

// ComputeWeierstrassY computes the y-coordinate for the Weierstrass curve P-256
// given x, using y = sqrt(x³ - 3x + B). Returns nil if the point is not on the curve.
func ComputeWeierstrassY(curve elliptic.Curve, x *big.Int) *big.Int {
	p := curve.Params().P

	x3 := new(big.Int).Mul(x, x)
	x3.Mod(x3, p)
	x3.Mul(x3, x)
	x3.Mod(x3, p)

	threeX := new(big.Int).Mul(x, big.NewInt(3))
	threeX.Mod(threeX, p)

	rhs := new(big.Int).Sub(x3, threeX)
	rhs.Mod(rhs, p)
	rhs.Add(rhs, curve.Params().B)
	rhs.Mod(rhs, p)

	if rhs.Sign() == 0 {
		return big.NewInt(0)
	}

	exp := new(big.Int).Add(p, big.NewInt(1))
	exp.Div(exp, big.NewInt(4))
	y := new(big.Int).Exp(rhs, exp, p)

	y2 := new(big.Int).Mul(y, y)
	y2.Mod(y2, p)
	if y2.Cmp(rhs) == 0 {
		return y
	}
	return nil
}

// PasswordToScalar maps a password string to a scalar in [1, N-1] via
// PBKDF2-HMAC-SHA256 with domain separation and tunable iterations. Never
// returns zero. The iteration count is configurable via QVOLE_KDF_ITERATIONS
// (default 600000). Both peers must use the same iteration count.
func PasswordToScalar(password string) *big.Int {
	pwBytes := []byte("qvole-spake2-pw:" + password)
	salt := []byte("qvole-spake2-pw:")
	h := pbkdf2.Key(pwBytes, salt, kdfIterations, 32, sha256.New)
	s := new(big.Int).SetBytes(h)
	s.Mod(s, Curve.Params().N)
	if s.Sign() == 0 {
		return big.NewInt(1)
	}
	return s
}

func generatorScalar(gx, gy *big.Int, scalar *big.Int) (x, y *big.Int) {
	return Curve.ScalarMult(gx, gy, scalar.Bytes())
}

// State holds ephemeral key material for one side of a SPAKE2 PAKE exchange.
type State struct {
	curve     elliptic.Curve
	pwScalar  *big.Int
	scalar    *big.Int
	blindedXM *big.Int
	blindedYM *big.Int
	blindedXN *big.Int
	blindedYN *big.Int
}

// NewState creates a new SPAKE2 state, generating a fresh ephemeral scalar
// and computing both M-based and N-based blinded points (y*G + w*M and y*G + w*N).
// The caller determines the protocol role after seeing the peer's points and
// uses the appropriate point via ComputeShared.
func NewState(password string) (*State, error) {
	curve := Curve
	pwScalar := PasswordToScalar(password)

	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		return nil, fmt.Errorf("spake2 rand: %w", err)
	}
	scalar := new(big.Int).SetBytes(priv)
	scalar.Mod(scalar, curve.Params().N)

	pubX, pubY := curve.ScalarBaseMult(priv)
	ZeroBytes(priv)

	bxm, bym := generatorScalar(Mx, My, pwScalar)
	blindedXM, blindedYM := curve.Add(pubX, pubY, bxm, bym)

	bxn, byn := generatorScalar(Nx, Ny, pwScalar)
	blindedXN, blindedYN := curve.Add(pubX, pubY, bxn, byn)

	return &State{
		curve:     curve,
		pwScalar:  pwScalar,
		scalar:    scalar,
		blindedXM: blindedXM,
		blindedYM: blindedYM,
		blindedXN: blindedXN,
		blindedYN: blindedYN,
	}, nil
}

// BlindedBytesM returns the marshalled M-based blinded public point.
func (s *State) BlindedBytesM() []byte {
	return elliptic.Marshal(s.curve, s.blindedXM, s.blindedYM)
}

// BlindedBytesN returns the marshalled N-based blinded public point.
func (s *State) BlindedBytesN() []byte {
	return elliptic.Marshal(s.curve, s.blindedXN, s.blindedYN)
}

// ZeroBytes zeroes every byte in b. Use //go:noinline to prevent the compiler
// from optimizing away the write.
//
//go:noinline
func ZeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Destroy attempts to zero sensitive big.Int fields.
// SetInt64(0) replaces the internal representation but does not guarantee
// wiping the old nat backing array; Go provides no safe mechanism for that.
// The critical source byte buffers are zeroed explicitly in NewState.
func (s *State) Destroy() {
	if s.pwScalar != nil {
		s.pwScalar.SetInt64(0)
	}
	if s.scalar != nil {
		s.scalar.SetInt64(0)
	}
	if s.blindedXM != nil {
		s.blindedXM.SetInt64(0)
	}
	if s.blindedYM != nil {
		s.blindedYM.SetInt64(0)
	}
	if s.blindedXN != nil {
		s.blindedXN.SetInt64(0)
	}
	if s.blindedYN != nil {
		s.blindedYN.SetInt64(0)
	}
}

// ComputeShared completes the SPAKE2 exchange using the correct generator.
// peerUsedM indicates whether the peer blinded with generator M (true) or N (false).
// Returns a 32-byte padded shared secret from the x-coordinate.
func (s *State) ComputeShared(peerBlindedBytes []byte, peerUsedM bool) ([]byte, error) {
	peerBlindedX, peerBlindedY := elliptic.Unmarshal(s.curve, peerBlindedBytes)
	if peerBlindedX == nil {
		return nil, fmt.Errorf("invalid peer spake2 point")
	}
	if !s.curve.IsOnCurve(peerBlindedX, peerBlindedY) {
		return nil, fmt.Errorf("peer spake2 point not on curve")
	}

	var pwx, pwy *big.Int
	if peerUsedM {
		pwx, pwy = s.curve.ScalarMult(Mx, My, s.pwScalar.Bytes())
	} else {
		pwx, pwy = s.curve.ScalarMult(Nx, Ny, s.pwScalar.Bytes())
	}
	pwyNeg := new(big.Int).Sub(s.curve.Params().P, pwy)
	unblindedX, unblindedY := s.curve.Add(peerBlindedX, peerBlindedY, pwx, pwyNeg)

	if unblindedX.Sign() == 0 && unblindedY.Sign() == 0 {
		return nil, fmt.Errorf("spake2: unblinded point is identity")
	}

	sharedX, _ := s.curve.ScalarMult(unblindedX, unblindedY, s.scalar.Bytes())
	if sharedX.Sign() == 0 {
		return nil, fmt.Errorf("spake2: shared x-coordinate is zero")
	}

	shared := sharedX.Bytes()
	if len(shared) < 32 {
		pad := make([]byte, 32)
		copy(pad[32-len(shared):], shared)
		shared = pad
	}
	return shared, nil
}

// SessionAAD returns the lexicographically-ordered concatenation of two public points.
// Operates on public data; variable-time comparison is safe.
func SessionAAD(myPoint, peerPoint []byte) []byte {
	a, b := myPoint, peerPoint
	if string(a) > string(b) {
		a, b = b, a
	}
	return append(a, b...)
}

func aadWithGenerators(myPoint, peerPoint []byte) []byte {
	aad := SessionAAD(myPoint, peerPoint)
	genBytes := elliptic.Marshal(Curve, Nx, Ny)
	return append(aad, genBytes...)
}

// DeriveSessionKey derives a 32-byte confirm HMAC key and a 32-byte AES-256-GCM
// encryption key from the SPAKE2 shared secret using HKDF.
func DeriveSessionKey(sharedSecret, myPoint, peerPoint []byte) (confirmKey, encKey []byte, err error) {
	aad := aadWithGenerators(myPoint, peerPoint)
	salt := sha256.Sum256(aad)
	reader := hkdf.New(sha256.New, sharedSecret, salt[:], []byte("qvole-spake2-session"))
	key := make([]byte, 64) // 32 for confirm HMAC, 32 for AES-256-GCM
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, nil, fmt.Errorf("hkdf read: %w", err)
	}
	return key[:32], key[32:], nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// EncryptMetadata encrypts data with AES-256-GCM using key and aad for authentication.
func EncryptMetadata(key, aad, data []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, aad), nil
}

// DecryptMetadata decrypts data with AES-256-GCM using key and aad for authentication.
func DecryptMetadata(key, aad, ciphertext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, actualCiphertext := ciphertext[:ns], ciphertext[ns:]
	return gcm.Open(nil, nonce, actualCiphertext, aad)
}

// ComputeConfirm computes an HMAC-SHA-256 over the ordered public points,
// the N generator, the peer's certificate fingerprint, a nonce, and a
// domain separation string. Returns an error if nonce is empty.
func ComputeConfirm(sessionKey, myPoint, peerPoint, nonce, peerFingerprint []byte) ([]byte, error) {
	if len(nonce) < 1 {
		return nil, fmt.Errorf("spake2: confirm nonce must be non-empty")
	}
	mac := hmac.New(sha256.New, sessionKey)
	a, b := myPoint, peerPoint
	if string(a) > string(b) { // public data; variable-time is safe
		a, b = b, a
	}
	mac.Write(a)
	mac.Write(b)
	genBytes := elliptic.Marshal(Curve, Nx, Ny)
	mac.Write(genBytes)
	mac.Write(peerFingerprint)
	mac.Write(nonce)
	mac.Write([]byte("qvole-spake2-confirm"))
	return mac.Sum(nil), nil
}

// VerifyConfirm checks that peerConfirm matches the expected HMAC computed
// with ComputeConfirm using constant-time comparison.
// myFingerprint is the local certificate fingerprint (the one the peer used
// when computing their confirm).
func VerifyConfirm(sessionKey, myPoint, peerPoint, nonce, myFingerprint, peerConfirm []byte) bool {
	expected, err := ComputeConfirm(sessionKey, myPoint, peerPoint, nonce, myFingerprint)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(expected, peerConfirm) == 1
}
