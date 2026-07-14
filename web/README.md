# cc-runtime web app

The browser surface for cc-runtime. It lists your agent sessions, answers the
question blocking an edit, and shows the notification feed from the same machine
or from your phone across the LAN or your tailnet.

It is a TypeScript single-page app built on
[`@cc-interact/react`](https://github.com/yasyf/cc-interact). The daemon builds
it into the Go binary and serves it off its HTTP plane.

## What it does

The sessions list shows every active subject with its status and open-question
count, read from `GET /api/sessions`. Open a session to see its questions and its
notification feed.

An open question renders with its prompt, the agent's reasoning, any diff, and
the selectable options. Submitting your answer posts to
`POST /api/subjects/{id}/answer`, which releases the edit gate the agent is
waiting on.

Notifications and live updates arrive over SSE. The app opens one `EventSource`
per subject against `/events` and resumes with `Last-Event-ID` after a drop.

Web Push is opt-in. The toggle subscribes to the daemon's VAPID key so a blocking
question reaches your device with the tab closed, and a service worker renders
the push frames.

## Access

The daemon serves the app off its auth-guarded HTTP plane. On loopback it is
open. Over the LAN or a tailnet it requires the pair bearer token, so pair from
the CLI to get a URL that carries it. The app reads the token once from that URL,
keeps it in `localStorage`, and attaches it as a `Bearer` header on every
request. An `EventSource` cannot set headers, so it falls back to a `?token=`
query. A tailnet reaches the daemon over HTTPS with its provisioned cert.

No relay sits in the middle. An earlier design routed clients through an online
bus and an outbound-registration relay; that design is gone. Clients now connect
straight to the daemon over loopback, the LAN, or the tailnet with the pair
token.

## Develop

```sh
npm install
# CC_RUNTIME_DEV_PORT is the daemon's HTTP port, from its handshake file.
CC_RUNTIME_DEV_PORT=<port> npm run dev
```

Build into the Go embed slot, run the tests, and typecheck:

```sh
npm run build     # writes ../internal/web/dist
npm test
npm run typecheck
```

`vite build` emits into `internal/web/dist`, which the daemon embeds and serves
with a deep-link fallback to `index.html`. A clean checkout carries only the
placeholder `index.html`, so `go build` needs no Node toolchain. `npm run build`
replaces it with the hashed assets.
