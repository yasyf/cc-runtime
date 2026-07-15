// A machine's live connection. The kit hands out ordered candidate URLs (LAN HTTPS
// first, tailnet HTTPS last) and cannot know which network is shared right now, so
// connecting means probing to resolve the reachable leg, then building the long-
// lived URLSessions the REST, SSE, and registrar clients ride. Each leg carries its
// own TLS handling: a pinned self-signed LAN leg, system trust for the tailnet leg.

import CcRuntimeKit
import Foundation
import Network

/// MachineConnection resolves and holds one machine's reachable endpoint. `connect`
/// probes the candidates; once connected it exposes the REST client and mints SSE
/// clients and a device-token registrar on the resolved leg's session.
@MainActor
@Observable
final class MachineConnection {
    /// State is the connection lifecycle the screen renders around.
    enum State: Equatable {
        case idle
        case connecting
        case connected
        case unreachable
    }

    let machine: Machine
    let token: String?

    private(set) var state: State = .idle
    private(set) var apiClient: APIClient?

    private var resolvedURL: URL?
    private var restSession: URLSession?
    private var sseSession: URLSession?

    init(machine: Machine, token: String?) {
        self.machine = machine
        self.token = token
    }

    /// connect probes the machine's candidates and, on the first that answers, builds
    /// the sessions and REST client for that leg. A prior success short-circuits; a
    /// failed probe lands on `.unreachable` for the caller to retry.
    func connect() async {
        if state == .connected {
            return
        }
        state = .connecting
        let prober = EndpointProber(machine: machine, token: token)
        guard let candidate = await prober.probe() else {
            state = .unreachable
            return
        }
        let url = candidate.baseURL
        let delegate = pinningDelegate(for: url)
        let rest = MachineConnection.session(sse: false, delegate: delegate)
        resolvedURL = url
        restSession = rest
        sseSession = MachineConnection.session(sse: true, delegate: delegate)
        apiClient = APIClient(baseURL: url, bearerToken: token, urlSession: rest)
        state = .connected
    }

    /// makeSSEClient builds a client for `subject`'s event stream on the resolved
    /// leg, reusing the SSE-tuned session so the stream lives indefinitely.
    func makeSSEClient(subject: String) -> SSEClient? {
        guard let resolvedURL, let sseSession else {
            return nil
        }
        return SSEClient(baseURL: resolvedURL, session: subject, bearerToken: token, urlSession: sseSession)
    }

    /// registrar posts this device's APNs token to the resolved leg.
    func registrar() -> DeviceTokenRegistrar? {
        guard let resolvedURL, let restSession else {
            return nil
        }
        return DeviceTokenRegistrar(baseURL: resolvedURL, bearerToken: token, urlSession: restSession)
    }

    private func pinningDelegate(for url: URL) -> CertificatePinningDelegate? {
        guard let fingerprint = machine.fingerprint, MachineConnection.isIPLiteral(url) else {
            return nil
        }
        return CertificatePinningDelegate(fingerprint: fingerprint)
    }

    /// isIPLiteral reports whether the URL's host is an IP literal — the signal that
    /// it is a self-signed LAN leg to pin rather than validate against system trust.
    static func isIPLiteral(_ url: URL) -> Bool {
        guard let host = url.host() else {
            return false
        }
        return IPv4Address(host) != nil || IPv6Address(host) != nil
    }

    private static func session(sse: Bool, delegate: CertificatePinningDelegate?) -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        if sse {
            config.timeoutIntervalForRequest = 60
            config.timeoutIntervalForResource = .infinity
            config.waitsForConnectivity = true
        } else {
            config.timeoutIntervalForRequest = 15
            config.waitsForConnectivity = false
        }
        return URLSession(configuration: config, delegate: delegate, delegateQueue: nil)
    }
}
