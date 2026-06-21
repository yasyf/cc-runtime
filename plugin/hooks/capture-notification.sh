#!/usr/bin/env bash
# Notification hook: mirror a native notification into the agent's subject log.
# Best-effort — the Notification hook does not gate any tool, so its exit code
# never blocks the session: a missing binary, a down daemon, or a version-skewed
# binary that fails to parse is harmless.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/cc-runtime"

[ -x "$BIN" ] || exit 0
exec "$BIN" capture-notification --source notification
