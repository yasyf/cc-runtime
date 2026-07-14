package access

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

func TestNeedsRefresh(t *testing.T) {
	tests := []struct {
		name     string
		notAfter time.Time
		want     bool
	}{
		{"far from expiry", testNow.Add(90 * 24 * time.Hour), false},
		{"one second outside the window", testNow.Add(certRefreshWindow + time.Second), false},
		{"exactly 30 days out", testNow.Add(certRefreshWindow), true},
		{"inside the window", testNow.Add(10 * 24 * time.Hour), true},
		{"already expired", testNow.Add(-time.Hour), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsRefresh(tt.notAfter, testNow); got != tt.want {
				t.Fatalf("needsRefresh(%v, %v) = %v, want %v", tt.notAfter, testNow, got, tt.want)
			}
		})
	}
}

// writeSelfSigned mints a self-signed cert for fqdn expiring at notAfter and
// writes cert.pem/key.pem into dir, mirroring what `tailscale cert` produces.
func writeSelfSigned(t *testing.T, dir, fqdn string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: fqdn},
		DNSNames:     []string{fqdn},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

// testProvider wires a CertProvider with a frozen clock and a counting fake
// provisioner that mints a cert expiring at notAfter.
func testProvider(t *testing.T, dir string, notAfter time.Time, calls *int) *CertProvider {
	t.Helper()
	p := NewCertProvider(dir, "h.ts.net")
	p.now = func() time.Time { return testNow }
	p.provision = func(_ context.Context, _, _, fqdn string) error {
		*calls++
		writeSelfSigned(t, dir, fqdn, notAfter)
		return nil
	}
	return p
}

func TestGetCertificateProvisionsOnceThenCaches(t *testing.T) {
	dir := t.TempDir()
	var calls int
	p := testProvider(t, dir, testNow.Add(90*24*time.Hour), &calls)

	cert, err := p.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if calls != 1 {
		t.Fatalf("provision calls = %d, want 1", calls)
	}
	if cert.Leaf.DNSNames[0] != "h.ts.net" {
		t.Fatalf("leaf DNS name = %q, want h.ts.net", cert.Leaf.DNSNames[0])
	}

	if _, err := p.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("GetCertificate (cached): %v", err)
	}
	if calls != 1 {
		t.Fatalf("provision calls after cached handshake = %d, want 1", calls)
	}
}

func TestGetCertificateServesValidDiskCertWithoutProvisioning(t *testing.T) {
	dir := t.TempDir()
	writeSelfSigned(t, dir, "h.ts.net", testNow.Add(60*24*time.Hour))
	p := NewCertProvider(dir, "h.ts.net")
	p.now = func() time.Time { return testNow }
	p.provision = func(context.Context, string, string, string) error {
		return errors.New("unexpected provision")
	}

	cert, err := p.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	want := testNow.Add(60 * 24 * time.Hour)
	if !cert.Leaf.NotAfter.Equal(want) {
		t.Fatalf("leaf NotAfter = %v, want %v", cert.Leaf.NotAfter, want)
	}
}

func TestGetCertificateReprovisionsNearExpiry(t *testing.T) {
	dir := t.TempDir()
	writeSelfSigned(t, dir, "h.ts.net", testNow.Add(10*24*time.Hour))
	var calls int
	fresh := testNow.Add(90 * 24 * time.Hour)
	p := testProvider(t, dir, fresh, &calls)

	cert, err := p.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if calls != 1 {
		t.Fatalf("provision calls = %d, want 1", calls)
	}
	if !cert.Leaf.NotAfter.Equal(fresh) {
		t.Fatalf("leaf NotAfter = %v, want the re-provisioned %v", cert.Leaf.NotAfter, fresh)
	}
}

func TestGetCertificateSurfacesProvisionError(t *testing.T) {
	p := NewCertProvider(t.TempDir(), "h.ts.net")
	p.now = func() time.Time { return testNow }
	boom := errors.New("acme fell over")
	p.provision = func(context.Context, string, string, string) error { return boom }

	if _, err := p.GetCertificate(&tls.ClientHelloInfo{}); !errors.Is(err, boom) {
		t.Fatalf("GetCertificate error = %v, want wrapped %v", err, boom)
	}
}
