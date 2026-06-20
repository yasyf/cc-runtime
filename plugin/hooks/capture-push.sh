#!/usr/bin/env bash
# PreToolUse(PushNotification) hook: mirror a native push notification into the
# agent's subject log. Best-effort — always exits 0 so it never blocks the
# native tool, even if the binary or daemon is unavailable.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/cc-runtime"

[ -x "$BIN" ] || exit 0
exec "$BIN" capture-notification --source push || exit 0
