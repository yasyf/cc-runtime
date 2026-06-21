#!/usr/bin/env bash
# PreToolUse(AskUserQuestion) hook: mirror a native AskUserQuestion into the
# agent's subject log so a soft-steer question persists and reaches remote
# clients instead of vanishing with the terminal. The native picker is answered
# in the terminal, so this is a one-way mirror — it never engages the edit gate.
# Best-effort — the exit code is non-blocking for PreToolUse (only exit 2 blocks
# the tool), so a missing binary, a down daemon, or a version-skewed binary that
# fails to parse never blocks the native AskUserQuestion.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin/cc-runtime"

[ -x "$BIN" ] || exit 0
exec "$BIN" capture-ask
