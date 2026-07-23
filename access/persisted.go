package access

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	persistedSchemaVersion = 1

	accessConfigSchemaIdentity    = "dev.yasyf.cc-runtime.access"
	accessConfigSchemaDescriptor  = "payload{bind:string<127.0.0.1|0.0.0.0>}"
	accessConfigSchemaFingerprint = "dev.yasyf.cc-runtime.access.729d1a3bd6b99aee47bd61e156a828cb20c942754523d8f93d7b12b3d9219bc1"

	apnsConfigSchemaIdentity    = "dev.yasyf.cc-runtime.apns"
	apnsConfigSchemaDescriptor  = "payload{bundleId:string,keyId:string,keyPath:string,sandbox:bool,teamId:string}"
	apnsConfigSchemaFingerprint = "dev.yasyf.cc-runtime.apns.58cf93d92743c92c13f1134a9b38770877117f8c048e2b80b44980a0f7f436b1"

	vapidConfigSchemaIdentity    = "dev.yasyf.cc-runtime.vapid"
	vapidConfigSchemaDescriptor  = "payload{private:string,public:string}"
	vapidConfigSchemaFingerprint = "dev.yasyf.cc-runtime.vapid.b8a47054d57122ca76211caa9f55303f612cecd9796a233551998d48ac3e1591"
)

type persistedEnvelope[Payload any] struct {
	Schema            string  `json:"schema"`
	SchemaVersion     int     `json:"schemaVersion"`
	SchemaFingerprint string  `json:"schemaFingerprint"`
	Payload           Payload `json:"payload"`
}

func decodePersisted[Payload any](
	data []byte,
	path string,
	identity string,
	fingerprint string,
) (Payload, error) {
	var envelope persistedEnvelope[Payload]
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope.Payload, fmt.Errorf("parse persisted state %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return envelope.Payload, fmt.Errorf("parse persisted state %q: trailing JSON value", path)
		}
		return envelope.Payload, fmt.Errorf("parse persisted state %q: %w", path, err)
	}
	if envelope.Schema != identity {
		return envelope.Payload, fmt.Errorf("persisted state %q: schema %q, want exactly %q", path, envelope.Schema, identity)
	}
	if envelope.SchemaVersion != persistedSchemaVersion {
		return envelope.Payload, fmt.Errorf(
			"persisted state %q: schema version %d, want exactly %d",
			path,
			envelope.SchemaVersion,
			persistedSchemaVersion,
		)
	}
	if envelope.SchemaFingerprint != fingerprint {
		return envelope.Payload, fmt.Errorf(
			"persisted state %q: schema fingerprint %q, want exactly %q",
			path,
			envelope.SchemaFingerprint,
			fingerprint,
		)
	}
	return envelope.Payload, nil
}

func encodePersisted[Payload any](identity string, fingerprint string, payload Payload) ([]byte, error) {
	return json.Marshal(persistedEnvelope[Payload]{
		Schema:            identity,
		SchemaVersion:     persistedSchemaVersion,
		SchemaFingerprint: fingerprint,
		Payload:           payload,
	})
}
