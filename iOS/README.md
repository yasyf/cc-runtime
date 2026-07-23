# cc-runtime iOS

The phone surface for cc-runtime. Pair a daemon with `cc-runtime pair`, then answer
its questions and receive its notifications from iOS. The app connects straight to
the daemon. Over the LAN it pins the daemon's self-signed HTTPS certificate; over
the tailnet it validates a real one. Push arrives directly via APNs, with no relay
in the path.

This directory holds the SwiftUI app and its client kit. Pair a daemon, browse its
sessions grouped awaiting-first, answer a question with its full context, and read a
per-machine notification feed built live from the event streams. The app registers
this device for APNs on connect and deep-links a tapped push to the question that
fired it.

## Layout

```
iOS/
├── CcRuntime.xcodeproj/     # Synced-folder project — files land in the target by living in the folder
├── CcRuntime/               # App target (module CcRuntimeApp)
│   ├── CcRuntimeApp.swift   #   @main entry point; roots MachinesView, shares the PushCenter
│   ├── Screens/             #   Machines, pairing (scan/paste/Bonjour), sessions, question detail, feed
│   ├── Support/             #   Connection, registry, view models, formatting, the stream hub
│   ├── Push/                #   AppDelegate + PushCenter — APNs registration and deep-link routing
│   ├── Info.plist           #   Bonjour _cc-runtime._tcp, local-network + camera usage
│   ├── CcRuntime.entitlements  # aps-environment for APNs
│   └── Assets.xcassets/
├── CcRuntimeTests/          # App-target unit tests (view models, parsing, ordering)
└── CcRuntimeKit/            # Local SwiftPM package — the network/model layer
    ├── Sources/CcRuntimeKit/
    └── Tests/CcRuntimeKitTests/
```

The project uses a filesystem-synchronized root group, so adding a Swift file to
`CcRuntime/` or `CcRuntimeTests/` puts it in the target with no `project.pbxproj`
edit. `CcRuntimeKit` is a plain Swift package: it builds and tests on macOS, so its
logic runs under `swift test` without a simulator.

## CcRuntimeKit

The client kit, unit-tested end to end:

- `PairPayload` decodes the `cc-runtime pair` QR into its `v`, `name`, `urls`,
  `token`, and `fp` fields, rejecting any version but 1. `urls` is the ordered
  candidate list, and `fp` is the optional LAN-leg certificate fingerprint.
- `Machine`, `MachineStore`, and `TokenStore` hold the paired-daemon roster in an
  exact, fingerprinted schema-v1 JSON envelope under Application Support, with
  each bearer token kept in the Keychain. Legacy, partial, or extended roster
  files fail instead of being imported or repaired. `forget(id:)` drops both the
  roster entry and the token.
- `EndpointProber` sequences a machine's candidate URLs and returns the first that
  answers `GET /api/sessions`. Each leg carries its own TLS handling. An IP-literal
  LAN leg pins to `fp`, while the tailnet leg uses system trust.
- `CertificatePinningDelegate` accepts only a server whose leaf certificate
  SHA-256(DER) equals `fp`, with no system-trust fallback for that host.
- `SSEClient` streams `GET /events`. Its byte-level parser survives hostile chunk
  boundaries, honors the `caught-up` boundary marker, resumes on `Last-Event-ID`,
  and reconnects with jittered backoff.
- `APIClient` speaks the REST plane. It lists `sessions`, reads `pending` and
  `openQuestions` that parse the string-encoded question payload, and posts an
  `answer`.
- `DeviceTokenRegistrar` POSTs the hex APNs token to `/api/push/device-tokens`.

## Screens

The app is one navigation stack rooted at the machine roster. Its view models live
in `Support/` behind narrow network protocols, so the ordering, grouping, answer, and
feed logic runs in the unit tests without a socket.

- The machine roster lists paired daemons with a reachability dot the `EndpointProber`
  resolves. Add one by scanning its QR, pasting the payload `cc-runtime pair` prints,
  or picking it off the Bonjour browser; swipe to forget, which drops the roster entry
  and the Keychain token together.
- The session list resolves the machine's reachable leg and groups its subjects
  awaiting-first. It polls the roster and streams every active subject, so the list and
  the notification feed stay live.
- Question detail renders a question's header, prompt, reasoning, and diff, with its
  options as single- or multi-select buttons alongside free-text and notes. Submitting
  drops the question optimistically and shows the idled banner once the subject
  releases its gate.
- The notification feed is built live from the subject streams and from arriving
  pushes; the `caught-up` marker gates each replay so history never doubles, and any
  entry dismisses or clears locally.
- On connect the app registers this device for push. It requests notification
  authorization, registers for remote notifications, and posts the hex token to the
  daemon, then deep-links a tapped alert to the subject it names.

## Build and test

Run the kit's tests on macOS, no simulator required:

```sh
cd iOS/CcRuntimeKit && swift test
```

Build and test the app for the simulator, naming one that `xcrun simctl list devices`
shows:

```sh
xcodebuild -project iOS/CcRuntime.xcodeproj -scheme CcRuntime \
  -destination 'platform=iOS Simulator,name=iPhone 17' build test
```

Formatting is SwiftFormat (`.swiftformat`); SwiftLint (`.swiftlint.yml`) owns the
judgment rules, including the force-unwrap ban, and surfaces everything as warnings.
