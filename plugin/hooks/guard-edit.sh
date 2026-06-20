#!/usr/bin/env bash
# PreToolUse(Edit|Write|NotebookEdit) hook: deny edits while an open question is
# awaiting an answer. Exits 2 (the PreToolUse block signal) on deny, 0 on allow.
# Fails open if the binary or daemon is unavailable.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/cc-runtime"

[ -x "$BIN" ] || exit 0
exec "$BIN" guard-edit
