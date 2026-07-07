package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func ServerConfig(caFile, certFile, keyFile string) (*tls.Config, error) {
	return ServerConfigWithClientAuth(caFile, certFile, keyFile, tls.RequireAndVerifyClientCert)
}

func ServerConfigWithClientAuth(caFile, certFile, keyFile string, clientAuth tls.ClientAuthType) (*tls.Config, error) {
	cert, pool, err := loadMaterial(caFile, certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   clientAuth,
	}, nil
}

func ClientConfig(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	cert, pool, err := loadMaterial(caFile, certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
	}, nil
}

func loadMaterial(caFile, certFile, keyFile string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("failed to parse CA PEM: %s", caFile)
	}
	return cert, pool, nil
}
