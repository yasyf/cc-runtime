package access

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestEnsureVAPIDMintsOnceAndPersists(t *testing.T) {
	st := Store{Dir: t.TempDir()}

	first, err := st.EnsureVAPID()
	if err != nil {
		t.Fatalf("EnsureVAPID: %v", err)
	}
	if first.Public == "" || first.Private == "" {
		t.Fatalf("minted keys = %+v, want non-empty public and private", first)
	}
	if first.Public == first.Private {
		t.Fatal("public and private keys must differ")
	}
	info, err := os.Stat(st.VAPIDPath())
	if err != nil {
		t.Fatalf("stat vapid file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("vapid file mode = %o, want 600", got)
	}
	written, err := os.ReadFile(st.VAPIDPath())
	if err != nil {
		t.Fatalf("read persisted VAPID keys: %v", err)
	}
	var envelope persistedEnvelope[VAPIDKeys]
	if err := json.Unmarshal(written, &envelope); err != nil {
		t.Fatalf("decode persisted VAPID envelope: %v", err)
	}
	if envelope.Schema != vapidConfigSchemaIdentity || envelope.SchemaVersion != persistedSchemaVersion ||
		envelope.SchemaFingerprint != vapidConfigSchemaFingerprint || envelope.Payload != first {
		t.Fatalf("persisted VAPID envelope = %+v, want exact v1 envelope for %+v", envelope, first)
	}

	second, err := st.EnsureVAPID()
	if err != nil {
		t.Fatalf("EnsureVAPID again: %v", err)
	}
	if second != first {
		t.Fatalf("second EnsureVAPID = %+v, want the persisted %+v", second, first)
	}

	other, err := Store{Dir: t.TempDir()}.EnsureVAPID()
	if err != nil {
		t.Fatalf("EnsureVAPID other store: %v", err)
	}
	if other == first {
		t.Fatal("distinct stores must mint distinct keypairs")
	}
}

func TestEnsureVAPIDFailsOnBadFile(t *testing.T) {
	for _, tc := range []struct {
		id      string
		content string
	}{
		{id: "corrupt json", content: "{not json"},
		{id: "legacy bare keys", content: `{"public":"public","private":"private"}`},
		{id: "extra envelope field", content: strings.TrimSuffix(validVAPIDEnvelope(t), "}") + `,"legacy":true}`},
		{id: "extra payload field", content: strings.Replace(validVAPIDEnvelope(t), `"private":"private"`, `"private":"private","legacy":true`, 1)},
		{id: "wrong identity", content: strings.Replace(validVAPIDEnvelope(t), vapidConfigSchemaIdentity, "foreign", 1)},
		{id: "wrong version", content: strings.Replace(validVAPIDEnvelope(t), `"schemaVersion":1`, `"schemaVersion":2`, 1)},
		{id: "wrong fingerprint", content: strings.Replace(validVAPIDEnvelope(t), vapidConfigSchemaFingerprint, "foreign", 1)},
		{id: "trailing value", content: validVAPIDEnvelope(t) + `{}`},
		{id: "missing key material", content: strings.Replace(validVAPIDEnvelope(t), `"public":"public"`, `"public":""`, 1)},
	} {
		t.Run(tc.id, func(t *testing.T) {
			st := Store{Dir: t.TempDir()}
			if err := os.WriteFile(st.VAPIDPath(), []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write vapid file: %v", err)
			}
			if _, err := st.EnsureVAPID(); err == nil {
				t.Fatal("EnsureVAPID on a bad file must fail loudly, got nil error")
			}
		})
	}
}

func validVAPIDEnvelope(t *testing.T) string {
	t.Helper()
	b, err := encodePersisted(
		vapidConfigSchemaIdentity,
		vapidConfigSchemaFingerprint,
		VAPIDKeys{Public: "public", Private: "private"},
	)
	if err != nil {
		t.Fatalf("encode valid VAPID envelope: %v", err)
	}
	return string(b)
}
