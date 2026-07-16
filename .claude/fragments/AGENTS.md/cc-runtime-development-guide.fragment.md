# cc-runtime Development Guide

A runtime that supplies Claude Code's harness-injected tools — AskUserQuestion, PushNotification, and more — with richer context and remote delivery from a web app or phone. It backs each harness tool with an implementation that outlives a single prompt: questions and notifications carry full context, reach you on a web app or phone, and persist for the session instead of vanishing when the prompt ends.

## Repository Structure

```
cc-runtime/
├── .github/workflows/  # CI (Go build, vet, race test)
├── .claude/            # Claude Code config — settings, guard hooks, jj config
├── .superset/          # Worktree bootstrap (env copy, direnv, jj init)
├── docs/               # Brand + project assets (mascot, banner, social card)
├── go.mod              # Module github.com/yasyf/cc-runtime
├── main.go             # Entrypoint
├── version/            # Build version metadata
├── runtime/            # Runtime core — harness-tool implementations
├── interaction/        # Interaction domain (questions, notifications)
├── tui/                # Terminal UI
├── plugin/             # Claude Code plugin — MCP channel tools + hooks
├── AGENTS.md           # This file — shared conventions
├── CLAUDE.md           # Claude-only rules; embeds AGENTS.md
├── STYLEGUIDE.md       # Concrete style rules
├── README.md           # Project overview
└── CHANGELOG.md        # Keep a Changelog history
```

cc-runtime is written in Go, built on cc-interact (github.com/yasyf/cc-interact).
