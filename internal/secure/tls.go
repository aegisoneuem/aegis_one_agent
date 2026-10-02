package secure

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
)

// mTLS transport credentials for the agent↔server gRPC channel. The certs may be
// from the product's managed PKI or the customer's own CA — this code only loads
// PEM files and does not care about the source. TLS 1.3 is enforced (principle:
// security mandatory, not toggleable).

const minTLS = tls.VersionTLS13

func loadCAPool(caPath string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read CA %q: %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in CA file %q", caPath)
	}
	return pool, nil
}

// ClientCredentials builds mTLS credentials for the agent: it presents the client
// cert/key and verifies the server against the CA. serverName must match the
// server certificate's SAN (e.g. "localhost").
func ClientCredentials(caPath, certPath, keyPath, serverName string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}
	pool, err := loadCAPool(caPath)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   minTLS,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
	}), nil
}

// ServerCredentials builds mTLS credentials for the server: it presents the
// server cert/key and REQUIRES + verifies a client cert against the CA.
func ServerCredentials(caPath, certPath, keyPath string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}
	pool, err := loadCAPool(caPath)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   minTLS,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}), nil
}
