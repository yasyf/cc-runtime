#!/usr/bin/env bash

set -euo pipefail

required=(
  HOMEBREW_TAP_TOKEN
  MACOS_SIGN_P12
  MACOS_SIGN_PASSWORD
  MACOS_NOTARY_ISSUER_ID
  MACOS_NOTARY_KEY_ID
  MACOS_NOTARY_KEY
)

for name in "${required[@]}"; do
  if [[ -z "${!name:-}" ]]; then
    echo "release preflight: missing $name" >&2
    exit 1
  fi
done

if [[ ! "$MACOS_NOTARY_ISSUER_ID" =~ ^[0-9A-Fa-f-]{36}$ ]]; then
  echo "release preflight: invalid MACOS_NOTARY_ISSUER_ID" >&2
  exit 1
fi
if [[ ! "$MACOS_NOTARY_KEY_ID" =~ ^[A-Z0-9]{10}$ ]]; then
  echo "release preflight: invalid MACOS_NOTARY_KEY_ID" >&2
  exit 1
fi

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

decode_base64() {
  local value="$1"
  local output="$2"
  if printf '%s' "$value" | base64 --decode >"$output" 2>/dev/null; then
    return
  fi
  printf '%s' "$value" | base64 -D >"$output"
}

decode_base64 "$MACOS_SIGN_P12" "$scratch/signing.p12"
decode_base64 "$MACOS_NOTARY_KEY" "$scratch/notary.p8"

extract_p12() {
  local mode="$1"
  local output="$2"
  if openssl pkcs12 -in "$scratch/signing.p12" "$mode" -nokeys \
    -passin env:MACOS_SIGN_PASSWORD -out "$output" 2>/dev/null; then
    return
  fi
  openssl pkcs12 -legacy -in "$scratch/signing.p12" "$mode" -nokeys \
    -passin env:MACOS_SIGN_PASSWORD -out "$output" 2>/dev/null
}

extract_p12 -clcerts "$scratch/leaf.pem"
extract_p12 -cacerts "$scratch/chain.pem"
openssl x509 -in "$scratch/leaf.pem" -checkend 0 -noout >/dev/null
openssl x509 -in "$scratch/chain.pem" -noout >/dev/null
openssl pkey -in "$scratch/notary.p8" -noout >/dev/null 2>&1

subject="$(openssl x509 -in "$scratch/leaf.pem" -noout -subject -nameopt RFC2253)"
issuer="$(openssl x509 -in "$scratch/leaf.pem" -noout -issuer -nameopt RFC2253)"

if [[ "$subject" != *"CN=Developer ID Application: Yasyf Mohamedali (SXKCTF23Q2)"* ]] ||
  [[ "$subject" != *"OU=SXKCTF23Q2"* ]]; then
  echo "release preflight: signing identity is not the cc-runtime Developer ID" >&2
  exit 1
fi
if [[ "$issuer" != *"CN=Developer ID Certification Authority"* ]]; then
  echo "release preflight: signing certificate has the wrong issuer" >&2
  exit 1
fi

echo "release preflight: Developer ID SXKCTF23Q2 and notarization inputs verified"
