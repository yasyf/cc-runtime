package access

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"
)

func TestEnsureLANCertMintsOnceAndPins(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	cert, err := st.EnsureLANCert()
	if err != nil {
		t.Fatalf("EnsureLANCert: %v", err)
	}

	fp := CertFingerprint(cert)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fp) {
		t.Fatalf("fingerprint = %q, want 64 hex chars", fp)
	}

	again, err := st.EnsureLANCert()
	if err != nil {
		t.Fatalf("EnsureLANCert (second): %v", err)
	}
	if CertFingerprint(again) != fp {
		t.Fatalf("second EnsureLANCert minted a different cert: %s != %s", CertFingerprint(again), fp)
	}

	fi, err := os.Stat(st.LANCertPath())
	if err != nil {
		t.Fatalf("stat cert file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cert file mode = %o, want 600", perm)
	}

	leaf := cert.Leaf
	if leaf.Subject.CommonName != "cc-runtime" {
		t.Errorf("CommonName = %q, want cc-runtime", leaf.Subject.CommonName)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != host {
		t.Errorf("DNSNames = %v, want [%s]", leaf.DNSNames, host)
	}
	now := time.Now()
	if leaf.NotBefore.After(now) {
		t.Errorf("NotBefore = %v, want before now", leaf.NotBefore)
	}
	wantExpiry := now.Add(lanCertValidity)
	if got := leaf.NotAfter; got.Before(wantExpiry.Add(-time.Hour)) || got.After(wantExpiry.Add(time.Hour)) {
		t.Errorf("NotAfter = %v, want ~%v", got, wantExpiry)
	}
	if leaf.IsCA {
		t.Error("cert is a CA, want a leaf")
	}
}

func TestEnsureLANCertConcurrentFirstRun(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	const n = 8
	fps := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cert, err := st.EnsureLANCert()
			if err != nil {
				t.Errorf("EnsureLANCert: %v", err)
				return
			}
			fps[i] = CertFingerprint(cert)
		}()
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if fps[i] != fps[0] {
			t.Fatalf("concurrent mints diverged: fps[%d] = %s, fps[0] = %s", i, fps[i], fps[0])
		}
	}
}

// TestLANCertServesPinnedTLS drives a TLS handshake against a listener serving
// the minted cert and verifies a client that pins the fingerprint accepts it.
func TestLANCertServesPinnedTLS(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	cert, err := st.EnsureLANCert()
	if err != nil {
		t.Fatalf("EnsureLANCert: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, conn)
		conn.Close()
	}()

	pin := CertFingerprint(cert)
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			if got := CertFingerprint(tls.Certificate{Leaf: leaf}); got != pin {
				t.Errorf("presented fingerprint = %s, want %s", got, pin)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
}
