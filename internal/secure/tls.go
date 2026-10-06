package secure

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
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
//
// The client private key is protected at rest: if `keys` is non-nil and only a
// plaintext key exists (e.g. embedded by the installer), it is sealed under the
// DPAPI-protected master key to keyPath+".enc" and the plaintext is securely
// removed. The key is then loaded from the encrypted copy. With keys==nil (or no
// master key available) it falls back to the plaintext key with a warning.
func ClientCredentials(caPath, certPath, keyPath, serverName string, keys KeyProvider) (credentials.TransportCredentials, error) {
	if keys != nil {
		if err := protectKeyAtRest(keys, keyPath); err != nil {
			return nil, err
		}
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read client cert %q: %w", certPath, err)
	}
	keyPEM, err := loadClientKeyPEM(keys, keyPath)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
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

// protectKeyAtRest encrypts a plaintext client key under the master key and
// removes the plaintext, leaving keyPath+".enc". It is idempotent: a no-op once
// the encrypted copy exists, or if no plaintext key is present.
func protectKeyAtRest(keys KeyProvider, keyPath string) error {
	encPath := keyPath + ".enc"
	if _, err := os.Stat(encPath); err == nil {
		return nil // already protected
	}
	plain, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to protect (maybe only .enc was provisioned — handled at load)
		}
		return fmt.Errorf("read client key %q: %w", keyPath, err)
	}
	mk, err := keys.Key()
	if err != nil {
		return fmt.Errorf("get master key to protect client key: %w", err)
	}
	blob, err := Seal(mk, plain)
	if err != nil {
		return fmt.Errorf("seal client key: %w", err)
	}
	// Self-check the round-trip BEFORE deleting the plaintext — never destroy the
	// only copy unless we can read it back.
	if back, oerr := Open(mk, blob); oerr != nil || !bytes.Equal(back, plain) {
		return fmt.Errorf("client key encryption self-check failed; left plaintext in place")
	}
	tmp := encPath + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return fmt.Errorf("write encrypted client key: %w", err)
	}
	if err := os.Rename(tmp, encPath); err != nil {
		return fmt.Errorf("install encrypted client key: %w", err)
	}
	if err := SecureDelete(keyPath); err != nil {
		slog.Warn("client key encrypted at rest, but could not remove the plaintext", "path", keyPath, "error", err.Error())
	} else {
		slog.Info("client key protected at rest (DPAPI-sealed)", "enc", encPath)
	}
	return nil
}

// loadClientKeyPEM returns the client private key PEM, preferring the encrypted
// copy (keyPath+".enc", decrypted with the master key) and falling back to a
// plaintext keyPath with a warning.
func loadClientKeyPEM(keys KeyProvider, keyPath string) ([]byte, error) {
	encPath := keyPath + ".enc"
	if blob, err := os.ReadFile(encPath); err == nil {
		if keys == nil {
			return nil, fmt.Errorf("encrypted client key present but no key provider to decrypt it")
		}
		mk, kerr := keys.Key()
		if kerr != nil {
			return nil, fmt.Errorf("get master key to decrypt client key: %w", kerr)
		}
		pem, oerr := Open(mk, blob)
		if oerr != nil {
			return nil, fmt.Errorf("decrypt client key %q: %w", encPath, oerr)
		}
		return pem, nil
	}
	pem, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read client key %q: %w", keyPath, err)
	}
	slog.Warn("mTLS client key is PLAINTEXT at rest — provide a key provider so it can be encrypted", "path", keyPath)
	return pem, nil
}

// ClientTLSServerAuth builds SERVER-AUTH-ONLY TLS credentials (verify the server
// via the CA, present no client cert). Used for the one-time enrollment call,
// where the agent does not yet have a client certificate.
func ClientTLSServerAuth(caPath, serverName string) (credentials.TransportCredentials, error) {
	pool, err := loadCAPool(caPath)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion: minTLS,
		RootCAs:    pool,
		ServerName: serverName,
	}), nil
}

// ServerTLSServerAuthOnly builds server-side TLS that presents the server cert
// but does NOT require a client cert — for the enrollment endpoint, where the
// agent has no client cert yet. (The main control-plane stays strict mTLS.)
func ServerTLSServerAuthOnly(certPath, keyPath string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   minTLS,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.NoClientCert,
	}), nil
}

// ServerCredentialsVerifyIfGiven builds server TLS that presents the server cert
// and VERIFIES a client cert against the CA when one is presented, but does not
// require it. This lets a single port serve both enrollment (no client cert yet,
// token-gated at the app layer) and the strict control plane (the app layer then
// requires a verified client cert for non-Enroll RPCs). TLS 1.3 enforced.
func ServerCredentialsVerifyIfGiven(caPath, certPath, keyPath string) (credentials.TransportCredentials, error) {
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
		ClientAuth:   tls.VerifyClientCertIfGiven,
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
