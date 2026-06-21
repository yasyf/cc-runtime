# cc-runtime web app (placeholder)

This directory is a placeholder for the cc-runtime web client. That client is the
browser surface for answering questions and clearing notifications remotely.

It is not built yet. The web app lands in P2, the online-bus and web-app phase. It
will be a TypeScript app on [`@cc-interact/react`](https://github.com/yasyf/cc-interact)
that lists sessions, answers an open question, and renders the notification feed,
reached through the outbound-registration relay.

Until then this holds only this README. No Node toolchain or CI is wired here yet.
That arrives with the P2 implementation.

See the project plan and `AGENTS.md` at the repo root for the full picture.
