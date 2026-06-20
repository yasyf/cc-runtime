#!/usr/bin/env bash
# Install the cc-runtime binary for this platform. No releases are published
# yet, so this never downloads: a local dev build in bin/ is used as-is, and
# its absence is a non-fatal note. Fail-open — always exits 0.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/cc-runtime"

if [ -x "$BIN" ]; then
  # Local dev build — leave it alone.
  exit 0
fi

echo "cc-runtime: no release binary available; build it into $BIN to enable the plugin" >&2
exit 0
