#!/usr/bin/env bash
# PreToolUse(PushNotification) hook: mirror a native push notification into the
# agent's subject log. Best-effort — the exit code is non-blocking for
# PreToolUse (only exit 2 blocks the tool), so a missing binary, a down daemon,
# or a version-skewed binary that fails to parse never blocks the native tool.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/cc-runtime"

[ -x "$BIN" ] || exit 0
exec "$BIN" capture-notification --source push
