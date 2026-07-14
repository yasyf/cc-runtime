package access

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// lanCertValidity is the self-signed LAN certificate's lifetime. The cert is
// an identity clients pin by fingerprint, not a CA-trusted credential, so a
// long life beats churning every paired client.
const lanCertValidity = 10 * 365 * 24 * time.Hour

// LANCertPath is the persisted self-signed keypair the LAN HTTPS listener
// serves: one PEM file holding the certificate block then the key block.
func (s Store) LANCertPath() string { return filepath.Join(s.Dir, "lan-cert.pem") }

// EnsureLANCert returns the self-signed certificate the LAN TLS listener
// serves, minting and persisting one the first time. Like EnsureToken it is
// race-safe: the fresh keypair publishes via an exclusive link, so concurrent
// first runs converge on the one certificate that landed on disk.
func (s Store) EnsureLANCert() (tls.Certificate, error) {
	cert, err := s.readLANCert()
	if err == nil {
		return cert, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return tls.Certificate{}, err
	}
	tmp, err := s.mintLANCertFile()
	if err != nil {
		return tls.Certificate{}, err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, s.LANCertPath()); err != nil && !errors.Is(err, fs.ErrExist) {
		return tls.Certificate{}, fmt.Errorf("publish LAN cert %q: %w", s.LANCertPath(), err)
	}
	return s.readLANCert()
}

// CertFingerprint is the SHA-256 of the leaf certificate's DER encoding, hex
// encoded — the pin the pairing payload hands a client so it can authenticate
// the self-signed LAN leg.
func CertFingerprint(cert tls.Certificate) string {
	sum := sha256.Sum256(cert.Leaf.Raw)
	return hex.EncodeToString(sum[:])
}

// readLANCert loads and parses the combined PEM. A missing file surfaces as
// fs.ErrNotExist so EnsureLANCert can mint.
func (s Store) readLANCert() (tls.Certificate, error) {
	b, err := os.ReadFile(s.LANCertPath())
	if err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair(b, b)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse LAN cert %q: %w", s.LANCertPath(), err)
	}
	return cert, nil
}

// mintLANCertFile generates a self-signed ECDSA P-256 keypair into a same-dir
// temp file (0600), creating the state dir if needed, and returns the temp
// path for the caller to publish.
func (s Store) mintLANCertFile() (string, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate LAN key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", fmt.Errorf("generate LAN cert serial: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		return "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "cc-runtime"},
		DNSNames:     []string{host},
		// Backdated so a client with a skewed clock accepts a fresh cert.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(lanCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", fmt.Errorf("mint LAN cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("encode LAN key: %w", err)
	}
	f, err := os.CreateTemp(s.Dir, ".lan-cert-")
	if err != nil {
		return "", fmt.Errorf("mint LAN cert: %w", err)
	}
	err = errors.Join(
		pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		f.Close(),
	)
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write LAN cert: %w", err)
	}
	return f.Name(), nil
}
