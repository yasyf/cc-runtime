# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- A machine mesh. `cc-runtime host add <user@host>` verifies the peer is
  reachable over ssh and has `cc-runtime` installed, records it in a
  flock-guarded `mesh.json`, and — unless `--no-recurse` — shells
  `cc-runtime host add <self> --no-recurse` on the peer so the registration
  is mutual. This host's ssh identity is detected from tailscale, or set with
  `--self`. `host list` shows registered peers with a live reachability and
  install column probed concurrently, and `host rm` drops a peer.
- Mesh-wide answering in the TUI. When the registry has peers, `cc-runtime
  tui` fans `interaction.list` across every machine, labels each awaiting
  subject with the machine it lives on, and resolves the one awaiting subject
  wherever it is. A subject on a peer has no reachable event stream, so its
  open questions are read over ssh and the answer routes back through
  `interaction.answer` on that peer, showing when it releases the remote gate.
  An unreachable peer is marked unreachable instead of blanking the list; with
  no peers registered the local-only answer surface is unchanged.
- `cc-runtime rpc <op> [--json <params>]` sends one envelope to the local
  daemon over its unix socket and prints the raw reply as one JSON line,
  exiting nonzero on an error reply — how a peer drives another over ssh. The
  op must be on a safe allowlist (`interaction.list`, `interaction.pending`,
  `interaction.answer`, `interaction.notify`, `mesh.presence`); control ops
  stay off the remote surface.
- Presence-routed delivery. When a question or urgency-high notification fires
  and this machine's console is unattended, the daemon walks the registered
  peers over ssh (`rpc mesh.presence`) and surfaces the interaction on the
  first attended peer via `rpc interaction.notify` — an origin-tagged
  notification the peer's clients see, carrying no bearer token. Answering
  still happens against the origin over the mesh answer path, so no state
  forks. The push lanes always fire regardless (phones are
  location-independent); routing only adds a peer-machine surface. A console
  is attended when this user owns an unlocked, unmirrored session — read from
  `ioreg` and `netstat` on macOS, unattended-with-a-reason elsewhere.
  `cc-runtime mesh route off` persists an opt-out in `mesh.json` without
  dropping the peers; routing is on by default wherever peers exist.
- Direct APNs delivery. `cc-runtime apns set --key <path>.p8 --key-id X
  --team-id Y --bundle-id Z` enables the lane, `--sandbox` targets Apple's
  sandbox environment, and `apns off` disables it. The daemon authenticates
  to APNs over HTTP/2 with an ES256 provider token minted from the auth key
  and refreshed before Apple's acceptance window closes. iOS clients
  register device tokens via `POST /api/push/device-tokens`, persisted as
  `push.device_register` events and deduped by token. Every question and
  notification fans out to registered devices alongside Web Push, and a
  device APNs reports unregistered with a 410 or a 400 `BadDeviceToken` is
  pruned behind a durable unregister event.
- The interaction ops now ride the daemon's auth-guarded HTTP plane:
  `GET /api/sessions` lists active subjects across scopes with open-question
  counts, `GET /api/subjects/{id}/pending` returns open questions with full
  payloads, and `POST /api/subjects/{id}/answer` maps onto the socket answer
  op with the same idempotent dedup and edit-gate release.
- The daemon serves an embedded single-page app at `/`, with deep links
  falling back to the shell. index.html, the assets, and the service worker
  serve outside the auth guard so a remote browser can bootstrap; `/events`
  and `/api` stay behind it. A committed placeholder keeps `go build` green
  until the real web build lands in `internal/web/dist`.
- `cc-runtime pair` exposes the daemon to the LAN over HTTPS behind a minted
  bearer token, prints a QR code plus copyable pair payload, and advertises
  `_cc-runtime._tcp` over Bonjour. `--off` returns to loopback only;
  `--reset-token` rotates the secret.
- A paired daemon on a tailscale node with MagicDNS also serves HTTPS on the
  tailscale interface (port 25443) with `tailscale cert`-minted certificates,
  re-provisioned within 30 days of expiry.
- Outbound Web Push. The daemon mints a VAPID keypair beside the pair token
  and serves its public key on `GET /api/push/vapid-key`. Browsers register
  subscriptions via `POST /api/push/subscriptions`, persisted as
  `push.subscribe` events and deduped by endpoint. Every question and
  notification fans out to all subscriptions, and an endpoint the push
  service reports gone with a 404 or 410 is pruned.
- Initial scaffolding.

### Security
- Subscription registration is bounded. The body caps at 8 KiB, endpoint and
  key lengths are limited, and the stored set caps at 32 subscriptions;
  re-registering a stored endpoint always passes.
- Device-token registration is bounded the same way. The body caps at 1 KiB,
  tokens must be even-length lowercase hex of 64 to 200 characters, and the
  stored set caps at 32 tokens. An APNs request carries only the ES256
  provider token; the pair bearer token never rides a push.
- Rotating the pair token or turning remote access off (`pair --off`) now
  revokes every stored Web Push subscription behind durable unsubscribe
  events, so a leaked bearer cannot buy push delivery forever.
- `pair` advertises private-range (RFC 1918) LAN addresses only; a globally
  routable interface address never enters the QR payload.
- The bearer token never crosses a network in cleartext. The daemon's plain
  HTTP plane stays loopback-only; the LAN leg serves HTTPS on port 25444 with
  a persisted self-signed certificate whose SHA-256 fingerprint rides the pair
  payload (`fp`) for clients to pin, alongside the tailscale-cert tailnet leg.

### Fixed
- Two concurrent answers to the same question can no longer split the event
  log from the projection: the loser of the deduplicated append projects the
  winner's answer.
- Concurrent first-run `pair` invocations converge on the one bearer token
  that landed on disk instead of each minting their own.
- `pair` verifies the running daemon's version and listener set before reuse.
  A stale daemon is upgraded, a daemon missing the tailnet TLS leg is
  restarted, and the QR carries the tailnet URL only when the handshake shows
  a live listener.
- Push delivery goroutines now run on the daemon lifecycle: shutdown cancels
  in-flight sends and drains them before the store closes.
- Builds no longer depend on an absolute local path: the temporary
  `cc-interact` replace is the relative sibling checkout, which CI clones.

[Unreleased]: https://github.com/yasyf/cc-runtime/commits/main
