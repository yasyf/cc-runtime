package access

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// certRefreshWindow is how close to expiry a cert may get before the provider
// re-provisions it on the next handshake.
const certRefreshWindow = 30 * 24 * time.Hour

// certProvisionTimeout bounds one `tailscale cert` run, which may perform a
// full ACME issuance on first use.
const certProvisionTimeout = 2 * time.Minute

// CertProvider serves the tailscale-minted TLS certificate for one FQDN,
// provisioning it on first use and re-provisioning when the leaf is within
// certRefreshWindow of expiry. now and provision are injected boundaries;
// NewCertProvider wires the real clock and the tailscale CLI.
type CertProvider struct {
	dir       string
	fqdn      string
	now       func() time.Time
	provision func(ctx context.Context, certFile, keyFile, fqdn string) error

	mu     sync.Mutex
	cached *tls.Certificate
}

// NewCertProvider returns a provider that mints certs for fqdn into dir via
// `tailscale cert`.
func NewCertProvider(dir, fqdn string) *CertProvider {
	return &CertProvider{dir: dir, fqdn: fqdn, now: time.Now, provision: tailscaleCert}
}

func (p *CertProvider) certPath() string { return filepath.Join(p.dir, "cert.pem") }
func (p *CertProvider) keyPath() string  { return filepath.Join(p.dir, "key.pem") }

// GetCertificate is the tls.Config callback: it returns the cached cert while
// it stays outside the refresh window, and (re-)provisions otherwise.
// Provisioning runs under its own timeout, not the handshake's context — the
// cert belongs to the daemon, and a client that gives up mid-issuance must not
// abort it.
func (p *CertProvider) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached != nil && !p.needsRefresh(p.cached) {
		return p.cached, nil
	}
	cert, err := p.load()
	if err == nil && !p.needsRefresh(cert) {
		p.cached = cert
		return cert, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), certProvisionTimeout)
	defer cancel()
	if err := p.provision(ctx, p.certPath(), p.keyPath(), p.fqdn); err != nil {
		return nil, fmt.Errorf("provision cert for %s: %w", p.fqdn, err)
	}
	if err := os.Chmod(p.keyPath(), 0o600); err != nil {
		return nil, err
	}
	cert, err = p.load()
	if err != nil {
		return nil, err
	}
	p.cached = cert
	return cert, nil
}

// load reads and parses the on-disk keypair. A missing cert file surfaces as
// fs.ErrNotExist so GetCertificate can provision.
func (p *CertProvider) load() (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(p.certPath(), p.keyPath())
	if err != nil {
		return nil, err
	}
	return &cert, nil
}

// needsRefresh reports whether the leaf is within certRefreshWindow of expiry.
func (p *CertProvider) needsRefresh(cert *tls.Certificate) bool {
	return needsRefresh(cert.Leaf.NotAfter, p.now())
}

// needsRefresh is the pure refresh decision: refresh once now + 30d reaches
// the leaf's NotAfter.
func needsRefresh(notAfter, now time.Time) bool {
	return !now.Add(certRefreshWindow).Before(notAfter)
}

// tailscaleCert shells `tailscale cert` to mint the keypair for fqdn.
func tailscaleCert(ctx context.Context, certFile, keyFile, fqdn string) error {
	cmd := exec.CommandContext(ctx, "tailscale", "cert", "--cert-file", certFile, "--key-file", keyFile, fqdn)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tailscale cert %s: %w: %s", fqdn, err, out)
	}
	return nil
}
