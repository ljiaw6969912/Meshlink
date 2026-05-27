package certutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CAOptions struct {
	OutDir string
	Name   string
	Days   int
}

type IssueOptions struct {
	OutDir    string
	Name      string
	CAPath    string
	CAKeyPath string
	DNSNames  []string
	IPAddrs   []string
	Days      int
}

type Result struct {
	Files []string `json:"files"`
}

func InitCA(opts CAOptions) (Result, error) {
	if opts.OutDir == "" {
		opts.OutDir = "certs"
	}
	if opts.Name == "" {
		opts.Name = "mesh-ca"
	}
	if opts.Days == 0 {
		opts.Days = 3650
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Result{}, fmt.Errorf("generate key: %w", err)
	}

	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: opts.Name},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().AddDate(0, 0, opts.Days),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return Result{}, fmt.Errorf("create cert: %w", err)
	}

	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return Result{}, err
	}
	certPath := filepath.Join(opts.OutDir, "ca.pem")
	keyPath := filepath.Join(opts.OutDir, "ca-key.pem")
	if err := writeCert(certPath, der); err != nil {
		return Result{}, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return Result{}, err
	}
	return Result{Files: []string{certPath, keyPath}}, nil
}

func Issue(opts IssueOptions) (Result, error) {
	if opts.OutDir == "" {
		opts.OutDir = "certs"
	}
	if opts.Name == "" {
		return Result{}, fmt.Errorf("missing node name")
	}
	if opts.CAPath == "" {
		opts.CAPath = filepath.Join("certs", "ca.pem")
	}
	if opts.CAKeyPath == "" {
		opts.CAKeyPath = filepath.Join("certs", "ca-key.pem")
	}
	if opts.Days == 0 {
		opts.Days = 825
	}

	caCert, err := readCert(opts.CAPath)
	if err != nil {
		return Result{}, err
	}
	caKey, err := readKey(opts.CAKeyPath)
	if err != nil {
		return Result{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Result{}, fmt.Errorf("generate key: %w", err)
	}

	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: opts.Name},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().AddDate(0, 0, opts.Days),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	for _, dns := range opts.DNSNames {
		dns = strings.TrimSpace(dns)
		if dns != "" {
			tpl.DNSNames = append(tpl.DNSNames, dns)
		}
	}
	for _, rawIP := range opts.IPAddrs {
		rawIP = strings.TrimSpace(rawIP)
		if rawIP == "" {
			continue
		}
		ip := net.ParseIP(rawIP)
		if ip == nil {
			return Result{}, fmt.Errorf("invalid IP SAN: %s", rawIP)
		}
		if ip.To4() == nil {
			return Result{}, fmt.Errorf("IPv6 IP SANs are not supported: %s", rawIP)
		}
		tpl.IPAddresses = append(tpl.IPAddresses, ip)
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return Result{}, fmt.Errorf("create cert: %w", err)
	}
	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return Result{}, err
	}
	certPath := filepath.Join(opts.OutDir, opts.Name+".pem")
	keyPath := filepath.Join(opts.OutDir, opts.Name+"-key.pem")
	if err := writeCert(certPath, der); err != nil {
		return Result{}, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return Result{}, err
	}
	return Result{Files: []string{certPath, keyPath}}, nil
}

func SplitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err)
	}
	return n
}

func writeCert(path string, der []byte) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("invalid certificate PEM: %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, fmt.Errorf("invalid EC private key PEM: %s", path)
	}
	return x509.ParseECPrivateKey(block.Bytes)
}
