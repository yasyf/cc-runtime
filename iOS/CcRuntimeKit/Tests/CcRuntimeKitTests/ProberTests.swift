@testable import CcRuntimeKit
import Foundation
import Testing

@Suite("Endpoint prober")
struct ProberTests {
    @Test("the first reachable candidate wins, in preference order")
    func firstReachableWins() async throws {
        let lanHost = "stub-\(UUID().uuidString).test"
        let tailHost = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: lanHost); StubURLProtocol.unregister(host: tailHost) }

        // The LAN leg is unreachable; the tailnet leg answers.
        StubURLProtocol.register(host: lanHost) { _ in .transportError }
        StubURLProtocol.register(host: tailHost) { _ in .json(200, #"{"subjects":[]}"#) }

        let lan = try candidate("https://\(lanHost):25444")
        let tail = try candidate("https://\(tailHost):25443")
        let prober = EndpointProber(candidates: [lan, tail], bearerToken: "tok")

        let winner = await prober.probe()
        #expect(winner?.baseURL == tail.baseURL)
    }

    @Test("the ordered-first candidate short-circuits when it answers")
    func firstCandidateShortCircuits() async throws {
        let firstHost = "stub-\(UUID().uuidString).test"
        let secondHost = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: firstHost); StubURLProtocol.unregister(host: secondHost) }

        let secondHit = SendableFlag()
        StubURLProtocol.register(host: firstHost) { _ in .json(200, #"{"subjects":[]}"#) }
        StubURLProtocol.register(host: secondHost) { _ in secondHit.set(); return .json(200, #"{"subjects":[]}"#) }

        let first = try candidate("https://\(firstHost):25444")
        let second = try candidate("https://\(secondHost):25443")
        let prober = EndpointProber(candidates: [first, second], bearerToken: "tok")

        let winner = await prober.probe()
        #expect(winner?.baseURL == first.baseURL)
        #expect(secondHit.value == false)
    }

    @Test("a non-2xx status does not count as an answer; probing falls through")
    func nonSuccessSkipped() async throws {
        let downHost = "stub-\(UUID().uuidString).test"
        let upHost = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: downHost); StubURLProtocol.unregister(host: upHost) }

        StubURLProtocol.register(host: downHost) { _ in .json(503, "unavailable") }
        StubURLProtocol.register(host: upHost) { _ in .json(200, #"{"subjects":[]}"#) }

        let downLeg = try candidate("https://\(downHost):25444")
        let upLeg = try candidate("https://\(upHost):25443")
        let prober = EndpointProber(candidates: [downLeg, upLeg], bearerToken: "tok")

        #expect(await prober.probe()?.baseURL == upLeg.baseURL)
    }

    @Test("the probe carries the bearer token on its Authorization header")
    func probeSendsBearer() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }
        let recorder = RequestRecorder()
        StubURLProtocol.register(host: host) { request in
            recorder.record(request)
            return .json(200, #"{"subjects":[]}"#)
        }

        let prober = try EndpointProber(candidates: [candidate("https://\(host):25444")], bearerToken: "sekret")
        _ = await prober.probe()

        let request = try #require(recorder.requests.first)
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer sekret")
        #expect(request.url?.path() == "/api/sessions")
    }

    @Test("no candidate answering returns nil")
    func noneAnswer() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }
        StubURLProtocol.register(host: host) { _ in .transportError }

        let prober = try EndpointProber(candidates: [candidate("https://\(host):25444")], bearerToken: nil)
        #expect(await prober.probe() == nil)
    }

    @Test("an http candidate is dropped: never probed, never handed the bearer")
    func httpCandidateDropped() async throws {
        let insecureHost = "stub-\(UUID().uuidString).test"
        let secureHost = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: insecureHost); StubURLProtocol.unregister(host: secureHost) }

        let insecureHit = SendableFlag()
        StubURLProtocol.register(host: insecureHost) { _ in insecureHit.set(); return .json(200, #"{"subjects":[]}"#) }
        StubURLProtocol.register(host: secureHost) { _ in .json(200, #"{"subjects":[]}"#) }

        let insecure = try candidate("http://\(insecureHost):25444")
        let secure = try candidate("https://\(secureHost):25443")
        let prober = EndpointProber(candidates: [insecure, secure], bearerToken: "sekret")

        #expect(await prober.probe()?.baseURL == secure.baseURL)
        #expect(insecureHit.value == false)
    }

    @Test("a prober whose only candidate is http returns nil without sending a request")
    func httpOnlyProberFindsNothing() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }
        let recorder = RequestRecorder()
        StubURLProtocol.register(host: host) { request in
            recorder.record(request)
            return .json(200, #"{"subjects":[]}"#)
        }

        let prober = try EndpointProber(candidates: [candidate("http://\(host):25444")], bearerToken: "sekret")
        #expect(await prober.probe() == nil)
        #expect(recorder.requests.isEmpty)
    }

    @Test(
        "a machine's IP-literal leg without a fingerprint — nil or empty — is dropped, never system-trusted",
        arguments: [nil, ""] as [String?]
    )
    func unpinnableIPLegDropped(fingerprint: String?) throws {
        let machine = try Machine(
            name: "tampered",
            urls: [
                #require(URL(string: "https://10.0.0.2:25444")),
                #require(URL(string: "https://mac.tail1a2b.ts.net:25443")),
            ],
            fingerprint: fingerprint
        )
        let candidates = EndpointProber.candidates(for: machine, probeTimeout: 1)
        #expect(candidates.map(\.baseURL) == [URL(string: "https://mac.tail1a2b.ts.net:25443")])
    }

    @Test("only IP-literal hosts are pinned; a hostname leg uses system trust")
    func pinsIPLiteralsOnly() throws {
        #expect(try EndpointProber.pins(#require(URL(string: "https://192.168.1.5:25444"))))
        #expect(try EndpointProber.pins(#require(URL(string: "https://[fe80::1]:25444"))))
        #expect(try !EndpointProber.pins(#require(URL(string: "https://mac.tail1a2b.ts.net:25443"))))
    }

    private func candidate(_ string: String) throws -> EndpointProber.Candidate {
        try EndpointProber.Candidate(baseURL: #require(URL(string: string)), session: StubURLProtocol.session())
    }
}

/// SendableFlag is a lock-guarded boolean a stub handler can set from the loading
/// thread and a test can read afterward.
final class SendableFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var flag = false

    func set() {
        lock.withLock { flag = true }
    }

    var value: Bool {
        lock.withLock { flag }
    }
}
