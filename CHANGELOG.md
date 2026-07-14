# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- `cc-runtime pair` exposes the daemon to the LAN behind a minted bearer token,
  prints a QR code plus copyable pair payload, and advertises `_cc-runtime._tcp`
  over Bonjour. `--off` returns to loopback only; `--reset-token` rotates the
  secret.
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

[Unreleased]: https://github.com/yasyf/cc-runtime/commits/main
