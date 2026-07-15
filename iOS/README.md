# cc-runtime iOS

The phone surface for cc-runtime. Pair a daemon with `cc-runtime pair`, then answer
its questions and receive its notifications from iOS. The app connects straight to
the daemon. Over the LAN it pins the daemon's self-signed HTTPS certificate; over
the tailnet it validates a real one. Push arrives directly via APNs, with no relay
in the path.

This directory holds the app foundations: the Xcode project, the app target, and the
network and model layer as a local Swift package. The screens land in a later stage,
so today this is the compiling skeleton plus a fully tested client kit.

## Layout

```
iOS/
├── CcRuntime.xcodeproj/     # Synced-folder project — files land in the target by living in the folder
├── CcRuntime/               # App target (module CcRuntimeApp)
│   ├── CcRuntimeApp.swift   #   @main entry point
│   ├── ContentView.swift    #   placeholder root view
│   ├── Info.plist           #   Bonjour _cc-runtime._tcp, local-network + camera usage
│   ├── CcRuntime.entitlements  # aps-environment for APNs
│   └── Assets.xcassets/
├── CcRuntimeTests/          # App-target smoke test
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
- `Machine`, `MachineStore`, and `TokenStore` hold the paired-daemon roster as JSON
  under Application Support, with each bearer token kept in the Keychain.
  `forget(id:)` drops both the roster entry and the token.
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

## Build and test

Run the kit's tests on macOS, no simulator required:

```sh
cd iOS/CcRuntimeKit && swift test
```

Build the app for the simulator, naming one that `xcrun simctl list devices` shows:

```sh
xcodebuild -project iOS/CcRuntime.xcodeproj -scheme CcRuntime \
  -destination 'platform=iOS Simulator,name=iPhone 17' build
```

Formatting is SwiftFormat (`.swiftformat`); SwiftLint (`.swiftlint.yml`) owns the
judgment rules, including the force-unwrap ban, and surfaces everything as warnings.
