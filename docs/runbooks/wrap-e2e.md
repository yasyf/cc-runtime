# Verify the wrap end-to-end flow in a real session

Use this runbook to prove the full `cc-runtime wrap claude` path works in a real interactive Claude Code session: the native `AskUserQuestion` and `PushNotification` are gone, the agent reaches for the `cc-runtime` `ask`/`notify` MCP tools instead, an `ask` gate-blocks edits until you answer, and notifications surface. Each step states the command to run and the exact thing to observe.

You need a built `cc-runtime` binary, the `claude` CLI, and a terminal you can answer questions in. The steps below were verified against `claude` 2.1.185. Budget about fifteen minutes.

## How wrap detection works

`cc-runtime wrap` sets one environment variable on the child `claude`: `CC_RUNTIME_WRAP=cc-runtime-wrap:v1` (the `WrapEnvVar` / `WrapSentinel` pair in `interaction/interaction.go`). That variable propagates to the MCP-launched `cc-runtime channel` subprocess the same way `CLAUDE_CODE_SESSION_ID` does, and the channel reads it in `launchedViaWrap` to pick its instruction set.

Detection is env-based, not transcript-based. A real run proved Claude Code never writes `--append-system-prompt` content into the session `.jsonl`, so the appended steer and its sentinel never reach the transcript. The env var is the signal of record. When you need to confirm detection fired, read the channel's instructions from the transcript's `mcp_instructions_delta` record, not the appended prompt.

## Build the binary and stage a channel config

Build the binary the plugin and the wrap launch both exec:

```bash
go build -o plugin/bin/cc-runtime .
```

Start the daemon so the channel server has something to attach to. It cold-starts on first use, but starting it up front makes the answer surface ready:

```bash
./plugin/bin/cc-runtime start
```

The output names your scope and points you at the answer surface:

```text
scope:  /Users/USER/Code/cc-runtime
answer: cc-runtime tui
```

Stage a minimal MCP config that launches the channel server by absolute path. This is the standalone equivalent of the installed plugin, whose `plugin.json` runs the same `cc-runtime channel` entrypoint:

```bash
BIN="$PWD/plugin/bin/cc-runtime"
CFG="$(mktemp -t ccmcp).json"
cat > "$CFG" <<EOF
{"mcpServers":{"cc-runtime":{"command":"$BIN","args":["channel"]}}}
EOF
```

If you run the installed plugin instead of `--mcp-config`, skip the staging block and let the plugin supply the channel. The rest of the runbook is identical.

## Launch claude through wrap

Launch an interactive session through `cc-runtime wrap`:

```bash
./plugin/bin/cc-runtime wrap claude --mcp-config "$CFG" --strict-mcp-config
```

Confirm three things once the session is up:

- Ask the agent to list its tools, or prompt it to ask you something. The native `AskUserQuestion` and `PushNotification` are absent — `cc-runtime wrap` removed them with `--disallowedTools`. The agent reaches for `mcp__cc-runtime__ask` and `mcp__cc-runtime__notify` instead.
- The `cc-runtime` channel shows the minimal wrapped instructions, opening "its ask and notify tools reach the human on the web app or phone". The unwrapped session opens with the longer "it backs Claude Code's harness-injected AskUserQuestion and PushNotification" steer, so the short form confirms detection fired.
- Run a `/Workflow` dynamic workflow and confirm it still runs. Wrap only touches the two interaction tools, so orchestration is untouched.

## Trigger an ask and release the gate

Give the agent a task that forces a question, such as a change with two plausible approaches. When it calls `ask`:

- Confirm the question reaches you on the answer surface, not just the terminal. Open it with `cc-runtime tui` in a second terminal.
- Confirm edits are gate-blocked while the question is open. Ask the agent to edit a file; the edit is refused until the question is answered.
- Answer in the TUI. The gate releases and the agent proceeds with your selection.

A single answered question flips the subject status from `awaiting` back to `idle`, which is the gate signal. Watch the status change in the TUI as you answer.

## Trigger a notify and a native flow

Prompt the agent to send a status update so it calls `notify`. Confirm the notification surfaces as an `interaction.notification` event on the answer surface, delivered remotely instead of only printed in the terminal.

Run a flow that the harness captures natively (a notification raised outside the agent's own `notify` call) and confirm it also lands as `interaction.notification`. Both the agent-driven and harness-driven paths converge on the same event type.

## Confirm auto-compaction still triggers

Run a long enough session that the context fills and Claude Code auto-compacts. Confirm compaction still triggers under wrap. Wrap changes the tool set and one environment variable, so the compaction trigger is unaffected, but a long real run is the only way to see it fire.

## What this runbook does not cover

The checks above need a human answering questions and a real interactive session, so they live here instead of in the test suite. Two facts are already verified by automation and need no manual repeat:

- `cc-runtime wrap` assembles the correct child argv and `claude` accepts `--disallowedTools AskUserQuestion,PushNotification` without a launch error. The `assembleWrap` unit tests in `runtime/wrap_test.go` pin the exact argv, and a real `claude -p` run launched clean.
- The channel server boots under the wrapped `claude` and reads `CC_RUNTIME_WRAP`. A wrapped `claude -p` run recorded the minimal wrapped instructions in its transcript's `mcp_instructions_delta`, while an unwrapped run recorded the full steer — the contrast that proves the env var reached the MCP subprocess.
