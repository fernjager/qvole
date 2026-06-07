package util

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestGenerateSelfSignedCert_Success(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test-qvole")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("no certificate bytes")
	}
}

func TestGenerateSelfSignedCert_KeyType(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	priv, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("expected ECDSA private key, got %T", cert.PrivateKey)
	}
	if priv.Curve != elliptic.P256() {
		t.Fatal("expected P-256 curve")
	}
}

func TestGenerateSelfSignedCert_ParseCertificate(t *testing.T) {
	cert, err := GenerateSelfSignedCert("qvole-test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate failed: %v", err)
	}
	if parsed.Subject.CommonName != "qvole-test" {
		t.Fatalf("expected CommonName 'qvole-test', got %q", parsed.Subject.CommonName)
	}
}

func TestGenerateSelfSignedCert_KeyUsage(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	parsed, _ := x509.ParseCertificate(cert.Certificate[0])
	if parsed.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Fatal("expected KeyUsageDigitalSignature")
	}
	if parsed.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
		t.Fatal("unexpected KeyUsageKeyEncipherment for ECDSA cert")
	}
}

func TestGenerateSelfSignedCert_ExtKeyUsage(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	parsed, _ := x509.ParseCertificate(cert.Certificate[0])
	foundServer := false
	foundClient := false
	for _, eku := range parsed.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			foundServer = true
		}
		if eku == x509.ExtKeyUsageClientAuth {
			foundClient = true
		}
	}
	if !foundServer {
		t.Fatal("expected ExtKeyUsageServerAuth")
	}
	if !foundClient {
		t.Fatal("expected ExtKeyUsageClientAuth")
	}
}

func TestCertFingerprint_Length(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	fp := CertFingerprint(cert)
	if len(fp) != 32 {
		t.Fatalf("expected 32-byte fingerprint, got %d", len(fp))
	}
}

func TestCertFingerprint_MatchesSHA256(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	fp := CertFingerprint(cert)
	expected := sha256.Sum256(cert.Certificate[0])
	if string(fp) != string(expected[:]) {
		t.Fatal("fingerprint does not match SHA-256 of DER bytes")
	}
}

func TestCertFingerprint_Empty(t *testing.T) {
	fp := CertFingerprint(tls.Certificate{})
	if fp != nil {
		t.Fatal("expected nil fingerprint for empty certificate")
	}
}

func TestGenerateSelfSignedCert_DeterministicNot(t *testing.T) {
	c1, _ := GenerateSelfSignedCert("test")
	c2, _ := GenerateSelfSignedCert("test")
	fp1 := CertFingerprint(c1)
	fp2 := CertFingerprint(c2)
	if string(fp1) == string(fp2) {
		t.Fatal("certificates should not be identical (random keys)")
	}
}

func TestGenerateSelfSignedCert_NotBeforeNotAfter(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	parsed, _ := x509.ParseCertificate(cert.Certificate[0])
	if parsed.NotBefore.After(parsed.NotAfter) {
		t.Fatal("NotBefore must be before NotAfter")
	}
}

func TestGenerateSelfSignedCert_SelfSigned(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	parsed, _ := x509.ParseCertificate(cert.Certificate[0])
	if err := parsed.CheckSignature(parsed.SignatureAlgorithm, parsed.RawTBSCertificate, parsed.Signature); err != nil {
		t.Fatalf("self-signature verification failed: %v", err)
	}
}

func TestCertFingerprint_StableAcrossParsing(t *testing.T) {
	cert, err := GenerateSelfSignedCert("test")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert failed: %v", err)
	}
	fp1 := CertFingerprint(cert)

	parsed, _ := x509.ParseCertificate(cert.Certificate[0])
	usingCert := tls.Certificate{
		Certificate: [][]byte{parsed.Raw},
		PrivateKey:  cert.PrivateKey,
	}
	fp2 := CertFingerprint(usingCert)

	if string(fp1) != string(fp2) {
		t.Fatal("fingerprint should be stable across parse/re-encode")
	}
}

func TestCertFingerprint_MultiDER(t *testing.T) {
	cert1, err := GenerateSelfSignedCert("first")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert: %v", err)
	}
	cert2, err := GenerateSelfSignedCert("second")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert: %v", err)
	}

	cert := tls.Certificate{
		Certificate: [][]byte{cert1.Certificate[0], cert2.Certificate[0]},
		PrivateKey:  cert1.PrivateKey,
	}
	fp := CertFingerprint(cert)
	expected := sha256.Sum256(cert1.Certificate[0])
	if string(fp) != string(expected[:]) {
		t.Fatal("fingerprint should match first DER, ignoring subsequent certs")
	}
}
