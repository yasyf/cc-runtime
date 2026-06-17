# cc-runtime

![cc-runtime banner](docs/assets/readme-banner.webp)

[![CI](https://img.shields.io/github/actions/workflow/status/yasyf/cc-runtime/ci.yml?branch=main&label=CI)](https://github.com/yasyf/cc-runtime/actions/workflows/ci.yml)
[![License: PolyForm-Noncommercial-1.0.0](https://img.shields.io/badge/License-PolyForm--Noncommercial--1.0.0-blue.svg)](https://github.com/yasyf/cc-runtime/blob/main/LICENSE)

A runtime that supplies Claude Code's harness-injected tools — AskUserQuestion, PushNotification, and more — with richer context and remote delivery from a web app or phone.

cc-runtime backs the tools the Claude Code harness injects into a session, replacing the thin built-ins with implementations that outlive any single prompt. A question carries the full reasoning and diff behind it and stays open until you answer; a notification reaches you wherever you are; and every interaction persists for the session instead of vanishing when the prompt ends. You see and act on all of it from one place, the terminal or a web app or your phone, so a long-running agent never stalls waiting for you to be at the keyboard.

## Install

cc-runtime is in early development. The implementation language and the way it plugs into Claude Code are still open — see [AGENTS.md](AGENTS.md) for the current state. There is nothing to install yet; this section gets the one-command install path the moment the first release ships.

## Quickstart

The first release ships a copy-pasteable example here: an agent asking a question or firing a notification, and you handling it from the web app with the expected output shown end to end. Until then, the pitch above and [AGENTS.md](AGENTS.md) are the spec.

## What problems does this solve?

- **You aren't always at the terminal.** A background agent that calls AskUserQuestion or wants to notify you stalls or goes unseen until you walk back to the keyboard. cc-runtime routes the call to your phone, so you act on it from anywhere.
- **Harness tools are thin and ephemeral.** The built-in AskUserQuestion shows a label and a few options, then it's gone if you miss it. cc-runtime attaches the full context behind each call and keeps it open for the whole session.
- **Every tool reinvents how it reaches you.** AskUserQuestion, PushNotification, and the rest each ship their own minimal surface. cc-runtime gives them one runtime: a shared web and phone surface, consistent delivery, and a record of what happened.
- **Approvals and alerts leave no trail.** When someone signs off on a change or an alert fires, nothing records it. cc-runtime logs who saw what, who approved what, and when.

## License

PolyForm-Noncommercial-1.0.0. See [LICENSE](https://github.com/yasyf/cc-runtime/blob/main/LICENSE).
