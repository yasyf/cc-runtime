# cc-ask

![cc-ask banner](docs/assets/readme-banner.webp)

[![CI](https://img.shields.io/github/actions/workflow/status/yasyf/cc-ask/ci.yml?branch=main&label=CI)](https://github.com/yasyf/cc-ask/actions/workflows/ci.yml)
[![License: PolyForm-Noncommercial-1.0.0](https://img.shields.io/badge/License-PolyForm--Noncommercial--1.0.0-blue.svg)](https://github.com/yasyf/cc-ask/blob/main/LICENSE)

A persistent, per-session alternative to AskUserQuestion with richer context and remote approvals from a web app or phone.

cc-ask gives a Claude Code agent a question channel that outlives the prompt. Every ask carries the full context behind it, the reasoning and the options and the diff or files under discussion, and stays open until you answer. You answer from wherever you are, the terminal or a web app or your phone, so a long-running agent never stalls waiting for you to be at the keyboard.

## Install

cc-ask is in early development. The implementation language and the way it plugs into Claude Code are still open — see [AGENTS.md](AGENTS.md) for the current state. There is nothing to install yet; this section gets the one-command install path the moment the first release ships.

## Quickstart

The first release ships a copy-pasteable example here: an agent posting a question, and you approving it from the web app with the expected output shown end to end. Until then, the pitch above and [AGENTS.md](AGENTS.md) are the spec.

## What problems does this solve?

- **You aren't always at the terminal.** A background agent that hits AskUserQuestion freezes until you walk back to the keyboard. cc-ask routes the question to your phone, so you unblock it from anywhere.
- **The built-in prompt is thin.** AskUserQuestion shows a label and a handful of options. cc-ask attaches the full context behind the ask, the reasoning and the diff and the files, so you decide with what the agent knows, not a summary of it.
- **Questions evaporate.** A missed or dismissed prompt is gone, and the agent is stuck. cc-ask keeps every open ask for the session, so you can answer late without losing the thread.
- **Approvals leave no trail.** When someone other than the driver signs off on a change, nothing records it. cc-ask logs who approved what, and when.

## License

PolyForm-Noncommercial-1.0.0. See [LICENSE](https://github.com/yasyf/cc-ask/blob/main/LICENSE).
