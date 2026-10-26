package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/fernjager/qvole/spake2"
)

// TestFingerprintSubstitutionRejected is the regression test for the relay
// fingerprint-substitution attack: each peer's confirm must bind both the
// sender's and receiver's certificate fingerprints. Merely echoing the
// receiver's fingerprint is not enough, because a relay can replace the
// sender's claimed fingerprint in the SPAKE2 payload.
func TestFingerprintSubstitutionRejected(t *testing.T) {
	const code = "fingerprint-binding-test-code"
	fpA := sha256.Sum256([]byte("certificate-A"))
	fpB := sha256.Sum256([]byte("certificate-B"))
	fpE := sha256.Sum256([]byte("attacker-certificate"))

	stateA, err := spake2.NewState(code)
	if err != nil {
		t.Fatal(err)
	}
	defer stateA.Destroy()
	stateB, err := spake2.NewState(code)
	if err != nil {
		t.Fatal(err)
	}
	defer stateB.Destroy()

	pointAM, pointAN := stateA.BlindedBytesM(), stateA.BlindedBytesN()
	pointBM, pointBN := stateB.BlindedBytesM(), stateB.BlindedBytesN()

	payloadA := append(append(append([]byte{}, pointAM...), pointAN...), fpA[:]...)
	payloadBGenuine := append(append(append([]byte{}, pointBM...), pointBN...), fpB[:]...)
	payloadBSubstituted := append(append(append([]byte{}, pointBM...), pointBN...), fpE[:]...)

	// B processes A's genuine payload.
	effMyB, effPeerB, fpFromA, _, ckB, ekB, err := processSpakeMsg(hex.EncodeToString(payloadA), stateB, pointBM, pointBN, fpB[:])
	if err != nil {
		t.Fatalf("B process: %v", err)
	}
	confirmB, err := buildConfirmPayload(ckB, ekB, effMyB, effPeerB, fpB[:], fpFromA, []byte("127.0.0.1:2000"))
	if err != nil {
		t.Fatalf("B build confirm: %v", err)
	}

	// A processes B's payload with the attacker's fingerprint substituted.
	effMyA, effPeerA, fpFromB, _, ckA, ekA, err := processSpakeMsg(hex.EncodeToString(payloadBSubstituted), stateA, pointAM, pointAN, fpA[:])
	if err != nil {
		t.Fatalf("A process substituted: %v", err)
	}
	if string(fpFromB) != string(fpE[:]) {
		t.Fatal("A did not see the substituted fingerprint")
	}
	if _, err := processConfirmMsg(hex.EncodeToString(confirmB), ckA, ekA, effMyA, effPeerA, fpA[:], fpFromB); err == nil {
		t.Fatal("VULNERABLE: A accepted B's confirm after B's fingerprint was substituted")
	}

	// The same confirm must still verify when the genuine fingerprint is used.
	effMyA2, effPeerA2, fpFromB2, _, ckA2, ekA2, err := processSpakeMsg(hex.EncodeToString(payloadBGenuine), stateA, pointAM, pointAN, fpA[:])
	if err != nil {
		t.Fatalf("A process genuine: %v", err)
	}
	if string(fpFromB2) != string(fpB[:]) {
		t.Fatal("A did not see B's genuine fingerprint")
	}
	if _, err := processConfirmMsg(hex.EncodeToString(confirmB), ckA2, ekA2, effMyA2, effPeerA2, fpA[:], fpFromB2); err != nil {
		t.Fatalf("A rejected B's genuine confirm: %v", err)
	}

	// A reflected payload carrying A's own fingerprint must be rejected before
	// any key derivation.
	reflected := append(append(append([]byte{}, pointBM...), pointBN...), fpA[:]...)
	if _, _, _, _, _, _, err := processSpakeMsg(hex.EncodeToString(reflected), stateA, pointAM, pointAN, fpA[:]); err == nil {
		t.Fatal("processSpakeMsg accepted a reflected certificate fingerprint")
	}
}
