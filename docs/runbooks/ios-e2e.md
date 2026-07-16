# Verify the iOS app end-to-end against a paired daemon

Use this runbook to prove the P3 iOS surface works with eyes and fingers on the real app: pairing lands a machine in the roster by all three paths, the session list survives a LAN drop, an answer from the phone releases the agent's edit gate, an APNs alert deep-links back to its question, a rotated token surfaces as an auth failure and recovers, and forgetting a machine leaves nothing behind. Each step states the command to run and the exact thing to observe.

You need a built `cc-runtime` binary, the app running on an iOS simulator or device, built per `iOS/README.md`, and a terminal `claude` session wired to the daemon to drive asks — the launch section of the [wrap runbook](wrap-e2e.md) covers that setup. The QR and failover steps need a physical device: a camera for the scan, and Tailscale on the phone for the tailnet leg. The APNs step needs an Apple-issued `.p8` auth key. Budget about thirty minutes.

## Pair the daemon three ways

Build the binary and expose the daemon:

```bash
go build -o plugin/bin/cc-runtime .
./plugin/bin/cc-runtime pair
```

`pair` prints the reachable addresses, a QR code, and the pairing payload as copyable JSON — the `v`, `name`, `urls`, `token`, and `fp` fields the app decodes. Pair by each path in turn, swipe-forgetting the machine between attempts so each one starts from scratch:

- Paste the JSON from the terminal into the app's manual pair form. The form decodes the payload as you paste and shows a parsed preview; confirm the preview names your machine and lists its URLs before you submit.
- Scan the QR code in the terminal with the device camera. Device-only — the simulator has no camera.
- Pick the machine off the Browse Network screen. The daemon advertises `_cc-runtime._tcp`, so it appears in the discovered list; tapping it opens the scanner, because discovery confirms the machine is on the network while the QR payload stays the credential. If nothing appears, allow Local Network access for the app in Settings.

After each pair, confirm the machine sits in the roster with a live reachability dot.

## Watch the session list fail over to the tailnet

Open the paired machine. The session list loads over the LAN leg — the first URL in the payload — and groups its subjects awaiting-first.

On a device with Tailscale up, turn the phone's Wi-Fi off so the LAN IP goes unreachable. The prober falls over to the `https://<host>.ts.net` URL and the list keeps loading; a list that still refreshes with Wi-Fi down is the failover proof. This check is device-only: a simulator shares the Mac's network, so its LAN leg never drops.

## Answer an ask from the phone

In the terminal session, give the agent a task that forces a question. When it calls `ask`:

- Confirm the question appears live in the app — the subject flips to awaiting, and question detail renders the header, prompt, and options.
- Answer from the phone. The question drops optimistically and the idled banner shows once the subject releases its gate.
- Back in the terminal, confirm the agent's blocked edit proceeds. The gate release is the round-trip proof.

## Land a push and follow the deep link

Prompt the agent to send a status update so it calls `notify`, and confirm the notification lands live in the app's feed.

Then configure the APNs lane with your Apple-issued auth key:

```bash
./plugin/bin/cc-runtime apns set --key /path/to/AuthKey_KEY_ID.p8 \
  --key-id KEY_ID --team-id TEAM_ID --bundle-id com.yasyf.cc-runtime --sandbox
```

`--sandbox` targets Apple's sandbox environment, where a development-signed build receives its pushes; drop it for a TestFlight or App Store build. Background the app and drive another ask. Confirm the alert renders as a real notification, then tap it and confirm the deep link lands on the question that fired it.

## Rotate the token and recover

Regenerate the bearer token:

```bash
./plugin/bin/cc-runtime pair --reset-token
```

The app's stored token is now stale. Confirm the app surfaces the auth failure instead of silently showing nothing, then re-pair with the fresh payload and confirm the machine comes back reachable.

## Forget the machine

Swipe-forget the machine in the roster, then relaunch the app. The roster stays empty — `forget` drops the roster entry and the Keychain token together, so an entry that stays gone across a relaunch is the observable for both.

## What this runbook does not cover

The automated P3 E2E already proved the wire behavior against a paired daemon on this machine, so none of it needs a manual repeat:

- The LAN leg with certificate-fingerprint pinning, plus the 401 negative after a token reset.
- The tailnet leg under system trust.
- The full round-trip through ask, awaiting, answer, and idle on the app's REST endpoints.
- Live SSE notification delivery on `/events?session=`.
- Device-token registration to `/api/push/device-tokens`, confirmed down to the projection row.
- `simctl push` delivery of the sender-shaped payload.
- The Go, kit, and app test suites, all green.

What automation could not do is drive the UI: the XcodeBuildMCP UI-automation tools were disabled and cua-driver lacked screen-recording permission, so no synthesized tap ever reached the simulator. Two environment notes for future runs:

- Enabling XcodeBuildMCP's UI automation lets a future run automate every check in this runbook.
- Live APNs delivery needs the Apple Developer `.p8` auth key — its key ID, the team ID, and the bundle ID `com.yasyf.cc-runtime`.
