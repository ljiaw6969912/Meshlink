package p2p

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crypto/x509/pkix"

	"meshlink/internal/certutil"
)

type sessionTestIdentity struct {
	tlsConfig   *tls.Config
	fingerprint string
}

// newSessionTestIdentities deliberately uses the production enrollment path.
// CreateCSR(Name: nodeID) and IssueCSR produce a device certificate with only
// Subject.CommonName, and no DNS SAN. This catches accidental reliance on
// Web-PKI hostname verification instead of Meshlink's CA + exact node binding.
func newSessionTestIdentities(t *testing.T, nodeIDs ...string) map[string]sessionTestIdentity {
	t.Helper()
	return newSessionTestIdentitiesWithDays(t, nil, nodeIDs...)
}

func newSessionTestIdentitiesWithDays(t *testing.T, daysByNode map[string]int, nodeIDs ...string) map[string]sessionTestIdentity {
	t.Helper()
	root := t.TempDir()
	caDir := filepath.Join(root, "ca")
	if _, err := certutil.InitCA(certutil.CAOptions{OutDir: caDir, Name: "meshlink-session-test-ca", Days: 2}); err != nil {
		t.Fatalf("initialize production-shape test CA: %v", err)
	}
	caPath := filepath.Join(caDir, "ca.pem")
	caKeyPath := filepath.Join(caDir, "ca-key.pem")
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read test CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("parse test CA pool")
	}

	identities := make(map[string]sessionTestIdentity, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		days := 1
		if configured, ok := daysByNode[nodeID]; ok {
			days = configured
		}
		nodeDir := filepath.Join(root, nodeID)
		csr, err := certutil.CreateCSR(certutil.CSROptions{OutDir: nodeDir, Name: nodeID})
		if err != nil {
			t.Fatalf("create production-shape CSR for %s: %v", nodeID, err)
		}
		issued, err := certutil.IssueCSR(certutil.IssueCSROptions{
			CSRPEM:    csr.CSRPEM,
			OutDir:    nodeDir,
			Name:      nodeID,
			CAPath:    caPath,
			CAKeyPath: caKeyPath,
			Days:      days,
		})
		if err != nil {
			t.Fatalf("issue production-shape certificate for %s: %v", nodeID, err)
		}
		certificate, err := tls.LoadX509KeyPair(issued.CertPath, csr.KeyPath)
		if err != nil {
			t.Fatalf("load %s key pair: %v", nodeID, err)
		}
		block, _ := pem.Decode(issued.CertPEM)
		if block == nil || block.Type != "CERTIFICATE" {
			t.Fatalf("decode %s certificate PEM", nodeID)
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse %s certificate: %v", nodeID, err)
		}
		if len(leaf.DNSNames) != 0 || len(leaf.IPAddresses) != 0 || leaf.Subject.CommonName != nodeID {
			t.Fatalf("%s certificate shape = CN %q DNS %v IP %v; want CN-only", nodeID, leaf.Subject.CommonName, leaf.DNSNames, leaf.IPAddresses)
		}
		certificate.Leaf = leaf
		identities[nodeID] = sessionTestIdentity{
			fingerprint: productionFingerprint(block.Bytes),
			tlsConfig: &tls.Config{
				MinVersion:   tls.VersionTLS13,
				Certificates: []tls.Certificate{certificate},
				RootCAs:      pool,
				ClientCAs:    pool,
				ClientAuth:   tls.RequireAndVerifyClientCert,
				NextProtos:   []string{sessionALPN},
			},
		}
	}
	return identities
}

func newSessionTestIdentitiesWithUsages(t *testing.T, usagesByNode map[string][]x509.ExtKeyUsage, nodeIDs ...string) map[string]sessionTestIdentity {
	t.Helper()
	now := time.Now().UTC()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate EKU test CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(9001),
		Subject:               pkix.Name{CommonName: "meshlink-session-eku-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatalf("create EKU test CA: %v", err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse EKU test CA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCertificate)
	identities := make(map[string]sessionTestIdentity, len(nodeIDs))
	for index, nodeID := range nodeIDs {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate %s EKU test key: %v", nodeID, err)
		}
		usages := usagesByNode[nodeID]
		if len(usages) == 0 {
			usages = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(int64(9002 + index)),
			Subject:      pkix.Name{CommonName: nodeID},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  usages,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCertificate, publicKey, caPrivate)
		if err != nil {
			t.Fatalf("create %s EKU test certificate: %v", nodeID, err)
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatalf("parse %s EKU test certificate: %v", nodeID, err)
		}
		identities[nodeID] = sessionTestIdentity{
			fingerprint: productionFingerprint(der),
			tlsConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
				Certificates: []tls.Certificate{{
					Certificate: [][]byte{der},
					PrivateKey:  privateKey,
					Leaf:        leaf,
				}},
				RootCAs:    pool,
				ClientCAs:  pool,
				ClientAuth: tls.RequireAndVerifyClientCert,
				NextProtos: []string{sessionALPN},
			},
		}
	}
	return identities
}

func productionFingerprint(der []byte) string {
	digest := sha256.Sum256(der)
	hexDigest := strings.ToUpper(hex.EncodeToString(digest[:]))
	parts := make([]string, 0, len(hexDigest)/2)
	for index := 0; index < len(hexDigest); index += 2 {
		parts = append(parts, hexDigest[index:index+2])
	}
	return "SHA256:" + strings.Join(parts, ":")
}
