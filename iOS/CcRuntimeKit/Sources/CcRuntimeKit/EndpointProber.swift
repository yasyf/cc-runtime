// The endpoint prober resolves which of a machine's candidate URLs is reachable
// right now. `cc-runtime pair` advertises an ordered list — LAN HTTPS legs first,
// the tailnet HTTPS leg last — and a client cannot know in advance which network it
// shares with the daemon. The prober sequences the candidates in preference order,
// probing GET /api/sessions with the bearer token, and returns the first that
// answers. Each leg carries the right TLS handling: an IP-literal LAN leg pins the
// self-signed certificate to the payload fingerprint, the tailnet leg uses system
// trust. The caller re-probes on connection loss by calling `probe()` again.

import Foundation
import Network

/// EndpointProber sequences a machine's candidate URLs and returns the first that
/// answers, along with the URLSession carrying that leg's certificate handling so
/// the caller reuses it for the APIClient and SSEClient.
public actor EndpointProber {
    /// Candidate is one URL to try together with the session that authenticates it.
    public struct Candidate: Sendable {
        public let baseURL: URL
        public let session: URLSession

        public init(baseURL: URL, session: URLSession) {
            self.baseURL = baseURL
            self.session = session
        }
    }

    private let candidates: [Candidate]
    private let bearerToken: String?
    private let probeTimeout: TimeInterval

    /// Creates a prober over an explicit candidate list (session-per-URL already
    /// resolved). Tests inject candidates whose sessions carry a stub URLProtocol.
    public init(candidates: [Candidate], bearerToken: String?, probeTimeout: TimeInterval = 5) {
        self.candidates = candidates
        self.bearerToken = bearerToken
        self.probeTimeout = probeTimeout
    }

    /// Creates a prober for `machine`, resolving each candidate URL's TLS handling:
    /// an IP-literal host (a self-signed LAN leg) pins to the machine fingerprint,
    /// any other host uses system trust. `token` rides on the probe's Authorization
    /// header.
    public init(machine: Machine, token: String?, probeTimeout: TimeInterval = 5) {
        let pinnedSession = machine.fingerprint.map { EndpointProber.pinnedSession(fingerprint: $0, timeout: probeTimeout) }
        let systemSession = EndpointProber.systemSession(timeout: probeTimeout)
        let candidates = machine.urls.map { url -> Candidate in
            if let pinnedSession, EndpointProber.pins(url) {
                return Candidate(baseURL: url, session: pinnedSession)
            }
            return Candidate(baseURL: url, session: systemSession)
        }
        self.init(candidates: candidates, bearerToken: token, probeTimeout: probeTimeout)
    }

    /// probe walks the candidates in order and returns the first whose GET
    /// /api/sessions answers with a 2xx, or nil when none does.
    public func probe() async -> Candidate? {
        for candidate in candidates where await answers(candidate) {
            return candidate
        }
        return nil
    }

    private func answers(_ candidate: Candidate) async -> Bool {
        var request = URLRequest(url: candidate.baseURL.appending(path: "api/sessions"))
        request.httpMethod = "GET"
        request.timeoutInterval = probeTimeout
        if let bearerToken {
            request.setValue("Bearer \(bearerToken)", forHTTPHeaderField: "Authorization")
        }
        guard
            let (_, response) = try? await candidate.session.data(for: request),
            let http = response as? HTTPURLResponse
        else {
            return false
        }
        return (200 ..< 300).contains(http.statusCode)
    }

    /// pins reports whether `url`'s host is an IP literal, the signal that it is a
    /// self-signed LAN leg to be pinned rather than validated against system trust.
    static func pins(_ url: URL) -> Bool {
        guard let host = url.host() else { return false }
        return IPv4Address(host) != nil || IPv6Address(host) != nil
    }

    private static func pinnedSession(fingerprint: String, timeout: TimeInterval) -> URLSession {
        URLSession(
            configuration: configuration(timeout: timeout),
            delegate: CertificatePinningDelegate(fingerprint: fingerprint),
            delegateQueue: nil
        )
    }

    private static func systemSession(timeout: TimeInterval) -> URLSession {
        URLSession(configuration: configuration(timeout: timeout))
    }

    private static func configuration(timeout: TimeInterval) -> URLSessionConfiguration {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = timeout
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.waitsForConnectivity = false
        return config
    }
}
