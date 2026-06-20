#!/usr/bin/env bash
# Notification hook: mirror a native notification into the agent's subject log.
# Best-effort — always exits 0 so it never blocks the session, even if the
# binary or daemon is unavailable.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/cc-runtime"

[ -x "$BIN" ] || exit 0
exec "$BIN" capture-notification --source notification || exit 0
