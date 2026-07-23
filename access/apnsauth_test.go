package access

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// writeAPNSKey mints a P-256 keypair and writes it as the PEM-encoded PKCS#8
// .p8 file Apple issues, returning the path and the key.
func writeAPNSKey(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate apns key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal apns key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey_TEST123.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write apns key: %v", err)
	}
	return path, key
}

func TestAPNSConfigRoundTrip(t *testing.T) {
	st := Store{Dir: t.TempDir()}

	if _, err := st.ReadAPNSConfig(); err == nil {
		t.Fatal("ReadAPNSConfig accepted an absent APNs config")
	}

	cfg := APNSConfig{KeyPath: "/keys/AuthKey_ABC123.p8", KeyID: "ABC123", TeamID: "TEAM99", BundleID: "com.example.cc", Sandbox: true}
	if err := st.WriteAPNSConfig(cfg); err != nil {
		t.Fatalf("WriteAPNSConfig: %v", err)
	}
	info, err := os.Stat(st.APNSConfigPath())
	if err != nil {
		t.Fatalf("stat apns config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("apns config mode = %o, want 600", got)
	}
	written, err := os.ReadFile(st.APNSConfigPath())
	if err != nil {
		t.Fatalf("read persisted apns config: %v", err)
	}
	want := `{"schema":"dev.yasyf.cc-runtime.apns","schemaVersion":1,"schemaFingerprint":"dev.yasyf.cc-runtime.apns.58cf93d92743c92c13f1134a9b38770877117f8c048e2b80b44980a0f7f436b1","payload":{"bundleId":"com.example.cc","keyId":"ABC123","keyPath":"/keys/AuthKey_ABC123.p8","sandbox":true,"teamId":"TEAM99"}}`
	if string(written) != want {
		t.Fatalf("persisted apns config = %s, want %s", written, want)
	}
	got, err := st.ReadAPNSConfig()
	if err != nil {
		t.Fatalf("ReadAPNSConfig: %v", err)
	}
	if got != cfg {
		t.Fatalf("ReadAPNSConfig = %+v, want %+v", got, cfg)
	}
	if !got.Enabled() {
		t.Fatal("a persisted config must report the lane enabled")
	}

	if err := st.ClearAPNSConfig(); err != nil {
		t.Fatalf("ClearAPNSConfig: %v", err)
	}
	cleared, err := st.ReadAPNSConfig()
	if err != nil {
		t.Fatalf("ReadAPNSConfig (cleared): %v", err)
	}
	if cleared.Enabled() {
		t.Fatalf("cleared config = %+v, want the disabled zero value", cleared)
	}
	disabled, err := os.ReadFile(st.APNSConfigPath())
	if err != nil {
		t.Fatalf("read disabled apns config: %v", err)
	}
	wantDisabled := `{"schema":"dev.yasyf.cc-runtime.apns","schemaVersion":1,"schemaFingerprint":"dev.yasyf.cc-runtime.apns.58cf93d92743c92c13f1134a9b38770877117f8c048e2b80b44980a0f7f436b1","payload":{"bundleId":"","keyId":"","keyPath":"","sandbox":false,"teamId":""}}`
	if string(disabled) != wantDisabled {
		t.Fatalf("disabled apns config = %s, want %s", disabled, wantDisabled)
	}
	if err := st.ClearAPNSConfig(); err != nil {
		t.Fatalf("ClearAPNSConfig (already disabled): %v", err)
	}
}

func TestReadAPNSConfigFailsLoudlyOnBrokenFile(t *testing.T) {
	validPayload := `{"bundleId":"com.example.cc","keyId":"ABC123","keyPath":"/k.p8","sandbox":false,"teamId":"TEAM99"}`
	valid := apnsConfigEnvelope(validPayload)
	for _, tc := range []struct {
		id      string
		content string
	}{
		{id: "corrupt json", content: "{not json"},
		{id: "old raw payload", content: validPayload},
		{id: "missing schema", content: `{"schemaVersion":1,"schemaFingerprint":"` + apnsConfigSchemaFingerprint + `","payload":` + validPayload + `}`},
		{id: "missing version", content: `{"schema":"` + apnsConfigSchemaIdentity + `","schemaFingerprint":"` + apnsConfigSchemaFingerprint + `","payload":` + validPayload + `}`},
		{id: "missing fingerprint", content: `{"schema":"` + apnsConfigSchemaIdentity + `","schemaVersion":1,"payload":` + validPayload + `}`},
		{id: "wrong schema", content: strings.Replace(valid, apnsConfigSchemaIdentity, "dev.yasyf.other", 1)},
		{id: "old version", content: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":0`, 1)},
		{id: "new version", content: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1)},
		{id: "wrong fingerprint", content: strings.Replace(valid, apnsConfigSchemaFingerprint, apnsConfigSchemaIdentity+".stale", 1)},
		{id: "missing payload", content: `{"schema":"` + apnsConfigSchemaIdentity + `","schemaVersion":1,"schemaFingerprint":"` + apnsConfigSchemaFingerprint + `"}`},
		{id: "null payload", content: apnsConfigEnvelope(`null`)},
		{id: "empty payload", content: apnsConfigEnvelope(`{}`)},
		{id: "missing bundle id", content: apnsConfigEnvelope(`{"keyId":"ABC123","keyPath":"/k.p8","sandbox":false,"teamId":"TEAM99"}`)},
		{id: "missing sandbox", content: apnsConfigEnvelope(`{"bundleId":"com.example.cc","keyId":"ABC123","keyPath":"/k.p8","teamId":"TEAM99"}`)},
		{id: "partial enabled config", content: apnsConfigEnvelope(`{"bundleId":"","keyId":"ABC123","keyPath":"/k.p8","sandbox":false,"teamId":"TEAM99"}`)},
		{id: "extra envelope field", content: strings.TrimSuffix(valid, "}") + `,"legacy":true}`},
		{id: "extra payload field", content: apnsConfigEnvelope(strings.TrimSuffix(validPayload, "}") + `,"legacy":true}`)},
		{id: "trailing value", content: valid + ` {}`},
	} {
		t.Run(tc.id, func(t *testing.T) {
			st := Store{Dir: t.TempDir()}
			if err := os.WriteFile(st.APNSConfigPath(), []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write apns config: %v", err)
			}
			if _, err := st.ReadAPNSConfig(); err == nil {
				t.Fatal("ReadAPNSConfig on a broken file must fail loudly, got nil error")
			}
		})
	}
}

func TestWriteAPNSConfigRejectsPartialEnabledConfig(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	if err := st.WriteAPNSConfig(APNSConfig{KeyID: "ABC123"}); err == nil {
		t.Fatal("WriteAPNSConfig accepted a partial enabled config")
	}
}

func apnsConfigEnvelope(payload string) string {
	return fmt.Sprintf(
		`{"schema":"%s","schemaVersion":1,"schemaFingerprint":"%s","payload":%s}`,
		apnsConfigSchemaIdentity,
		apnsConfigSchemaFingerprint,
		payload,
	)
}

func TestLoadAPNSKeyParsesP8(t *testing.T) {
	path, key := writeAPNSKey(t)
	got, err := LoadAPNSKey(path)
	if err != nil {
		t.Fatalf("LoadAPNSKey: %v", err)
	}
	if !got.Equal(key) {
		t.Fatal("loaded key does not match the minted key")
	}
}

func TestLoadAPNSKeyRejectsBadKeys(t *testing.T) {
	p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate p-256 key: %v", err)
	}
	sec1, err := x509.MarshalECPrivateKey(p256)
	if err != nil {
		t.Fatalf("marshal sec1 key: %v", err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	edDER, err := x509.MarshalPKCS8PrivateKey(edKey)
	if err != nil {
		t.Fatalf("marshal ed25519 key: %v", err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate p-384 key: %v", err)
	}
	p384DER, err := x509.MarshalPKCS8PrivateKey(p384)
	if err != nil {
		t.Fatalf("marshal p-384 key: %v", err)
	}

	for _, tc := range []struct {
		id      string
		content []byte
	}{
		{id: "not pem", content: []byte("garbage")},
		{id: "wrong pem type", content: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})},
		{id: "pkcs8 but not ecdsa", content: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: edDER})},
		{id: "ecdsa but not p-256", content: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p384DER})},
	} {
		t.Run(tc.id, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "AuthKey_BAD.p8")
			if err := os.WriteFile(path, tc.content, 0o600); err != nil {
				t.Fatalf("write key file: %v", err)
			}
			if _, err := LoadAPNSKey(path); err == nil {
				t.Fatal("LoadAPNSKey must reject the key, got nil error")
			}
		})
	}

	t.Run("absent file", func(t *testing.T) {
		if _, err := LoadAPNSKey(filepath.Join(t.TempDir(), "missing.p8")); err == nil {
			t.Fatal("LoadAPNSKey on a missing file must fail, got nil error")
		}
	})
}

func TestBearerMintsVerifiableES256Token(t *testing.T) {
	path, key := writeAPNSKey(t)
	loaded, err := LoadAPNSKey(path)
	if err != nil {
		t.Fatalf("LoadAPNSKey: %v", err)
	}
	auth := &apnsAuth{key: loaded, keyID: "ABC123", teamID: "TEAM99"}
	now := time.Unix(1_752_000_000, 0)

	tok, err := auth.bearer(now)
	if err != nil {
		t.Fatalf("bearer: %v", err)
	}
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		return &key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuedAt())
	if err != nil {
		t.Fatalf("parse provider token: %v", err)
	}
	if !parsed.Valid {
		t.Fatal("provider token did not verify against the .p8 public key")
	}
	if got := parsed.Header["alg"]; got != "ES256" {
		t.Fatalf("alg = %v, want ES256", got)
	}
	if got := parsed.Header["kid"]; got != "ABC123" {
		t.Fatalf("kid = %v, want ABC123", got)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if got := claims["iss"]; got != "TEAM99" {
		t.Fatalf("iss = %v, want TEAM99", got)
	}
	if got := claims["iat"]; got != float64(now.Unix()) {
		t.Fatalf("iat = %v, want %d", got, now.Unix())
	}
	if len(claims) != 2 {
		t.Fatalf("claims = %v, want exactly iss and iat", claims)
	}
}

func TestBearerRefreshesBeforeAppleWindowExpires(t *testing.T) {
	_, key := writeAPNSKey(t)
	t0 := time.Unix(1_752_000_000, 0)
	for _, tc := range []struct {
		id        string
		offset    time.Duration
		wantFresh bool
	}{
		{id: "immediately reuses", offset: 0, wantFresh: false},
		{id: "just inside the window reuses", offset: apnsTokenRefresh - time.Second, wantFresh: false},
		{id: "at the refresh threshold re-mints", offset: apnsTokenRefresh, wantFresh: true},
		{id: "well past the threshold re-mints", offset: 55 * time.Minute, wantFresh: true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			auth := &apnsAuth{key: key, keyID: "ABC123", teamID: "TEAM99"}
			first, err := auth.bearer(t0)
			if err != nil {
				t.Fatalf("bearer (mint): %v", err)
			}
			got, err := auth.bearer(t0.Add(tc.offset))
			if err != nil {
				t.Fatalf("bearer (+%s): %v", tc.offset, err)
			}
			if !tc.wantFresh {
				if got != first {
					t.Fatalf("bearer at +%s re-minted, want the cached token", tc.offset)
				}
				return
			}
			if got == first {
				t.Fatalf("bearer at +%s reused the cached token, want a fresh mint", tc.offset)
			}
			parsed, err := jwt.Parse(got, func(t *jwt.Token) (any, error) {
				return &key.PublicKey, nil
			}, jwt.WithValidMethods([]string{"ES256"}))
			if err != nil {
				t.Fatalf("parse re-minted token: %v", err)
			}
			if iat := parsed.Claims.(jwt.MapClaims)["iat"]; iat != float64(t0.Add(tc.offset).Unix()) {
				t.Fatalf("re-minted iat = %v, want %d", iat, t0.Add(tc.offset).Unix())
			}
		})
	}
}
