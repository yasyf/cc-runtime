# ![cc-runtime](docs/assets/readme-banner.webp)

**Stop babysitting the terminal. Your agent's questions ping your phone.** cc-runtime routes AskUserQuestion and PushNotification through a daemon; you tap an answer from web or phone and the run keeps moving.

[![CI](https://github.com/yasyf/cc-runtime/actions/workflows/ci.yml/badge.svg)](https://github.com/yasyf/cc-runtime/actions/workflows/ci.yml)
[![PolyForm Noncommercial license](https://img.shields.io/badge/license-PolyForm--Noncommercial--1.0.0-blue)](LICENSE)

## Get started

```bash
go install github.com/yasyf/cc-runtime@latest
```

```text
$ cc-runtime --help
Richer, persistent, remotely-answerable Claude Code harness tools

Available Commands:
  start                Start the cc-runtime daemon
  status               Show daemon and subject status
  stop                 Stop the background daemon
  tui                  Answer the agent's questions in an interactive terminal UI
  watch                Stream subject events as line-delimited JSON (one event per line)
  wrap                 Run claude with the native ask/notify tools disabled and steered to cc-runtime
```

Driving with an agent? Paste this:

```text
Install cc-runtime with `go install github.com/yasyf/cc-runtime@latest`, start the
daemon with `cc-runtime start`, and confirm it with `cc-runtime status`. I'll launch
wrapped sessions with `cc-runtime wrap claude` and answer from `cc-runtime tui`.
Docs: https://github.com/yasyf/cc-runtime
```

---

## Use cases

### Unblock a long-running agent without walking back to the keyboard

A background agent that calls AskUserQuestion stalls in a terminal you're not looking at, and the question dies with the prompt. Launch the session wrapped instead:

```bash
cc-runtime wrap claude
```

The wrapper strips the native ask/notify tools and steers the agent to cc-runtime's, so every question lands in the daemon and stays open until you answer, with an edit gate holding the agent's writes in the meantime.

### See the reasoning and diff behind every question, not just three option labels

The native picker shows a header chip and a few option labels; you approve or reject without seeing what hinges on the answer. Open the answer surface instead:

```bash
cc-runtime tui
```

Each question card renders the agent's prompt, its reasoning, the option list with per-option previews, and the unified diff it wants you to weigh. The native tool never carries that context, and any terminal that shares the scope can answer.

### Keep a record of what was approved, and when

An approval given in the native picker vanishes the moment the prompt ends. cc-runtime appends every question, answer, and notification to the subject's event log, and `watch` tails it live.

```bash
cc-runtime watch
```

Events stream as line-delimited JSON, so the selected options, free-text notes, and timestamps outlive the session.

---

## How it's built

cc-runtime is a Go daemon on the [cc-interact](https://github.com/yasyf/cc-interact) substrate. The Claude Code plugin layer adds an MCP channel exposing `ask` and `notify` in place of the native tools, plus hooks that mirror native AskUserQuestion and PushNotification calls into the event log and enforce the edit gate. The Bubble Tea TUI is the answer surface today; the web app and the iOS client with push delivery are the next two phases.

cc-runtime is pre-release; expect breaking changes until the first tagged release.

Licensed under [PolyForm Noncommercial 1.0.0](LICENSE).
