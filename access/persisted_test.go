package access

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestPersistedSchemaFingerprints(t *testing.T) {
	for _, tc := range []struct {
		name        string
		identity    string
		descriptor  string
		fingerprint string
	}{
		{
			name:        "access",
			identity:    accessConfigSchemaIdentity,
			descriptor:  accessConfigSchemaDescriptor,
			fingerprint: accessConfigSchemaFingerprint,
		},
		{
			name:        "apns",
			identity:    apnsConfigSchemaIdentity,
			descriptor:  apnsConfigSchemaDescriptor,
			fingerprint: apnsConfigSchemaFingerprint,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := sha256.Sum256([]byte(tc.identity + "\x00v1\x00" + tc.descriptor))
			want := tc.identity + "." + hex.EncodeToString(digest[:])
			if tc.fingerprint != want {
				t.Fatalf("fingerprint = %q, want %q", tc.fingerprint, want)
			}
		})
	}
}
