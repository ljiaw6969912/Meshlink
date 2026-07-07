package certutil

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateCSRAndIssueCSRKeepsPrivateKeyOnSpoke(t *testing.T) {
	dir := t.TempDir()
	if _, err := InitCA(CAOptions{OutDir: dir, Name: "mesh-ca"}); err != nil {
		t.Fatal(err)
	}

	csrResult, err := CreateCSR(CSROptions{
		OutDir: dir,
		Name:   "laptop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "laptop-key.pem")); err != nil {
		t.Fatalf("private key was not written on spoke side: %v", err)
	}

	issued, err := IssueCSR(IssueCSROptions{
		CSRPEM:    csrResult.CSRPEM,
		CAPath:    filepath.Join(dir, "ca.pem"),
		CAKeyPath: filepath.Join(dir, "ca-key.pem"),
		Days:      825,
	})
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCertificateForTest(t, issued.CertPEM)
	csr := parseCSRForTest(t, csrResult.CSRPEM)
	if cert.Subject.CommonName != "laptop" {
		t.Fatalf("certificate common name = %q, want laptop", cert.Subject.CommonName)
	}
	certKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("certificate public key type = %T, want *ecdsa.PublicKey", cert.PublicKey)
	}
	if !certKey.Equal(csr.PublicKey) {
		t.Fatal("issued certificate public key does not match CSR")
	}
	if len(issued.PrivateKeyPEM) != 0 {
		t.Fatal("hub-side CSR signing must not return a private key")
	}
}

func TestCreateCSRRejectsIPv6SAN(t *testing.T) {
	_, err := CreateCSR(CSROptions{
		OutDir:  t.TempDir(),
		Name:    "laptop",
		IPAddrs: []string{"fd77::2"},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func parseCertificateForTest(t *testing.T, b []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("invalid certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func parseCSRForTest(t *testing.T, b []byte) *x509.CertificateRequest {
	t.Helper()
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return csr
}
