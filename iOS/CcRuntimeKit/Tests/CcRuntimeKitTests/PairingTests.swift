@testable import CcRuntimeKit
import Foundation
import Testing

@Suite("Pairing")
struct PairingTests {
    @Test("a well-formed v1 payload decodes, preserving URL order and the fingerprint")
    func pairPayloadDecodesGood() throws {
        let json = #"""
        {"v":1,"name":"mac-studio","urls":["https://192.168.1.5:25444","https://mac.tail1a2b.ts.net:25443"],"token":"secret-abc","fp":"de7be0e6"}
        """#
        let payload = try JSONDecoder().decode(PairPayload.self, from: Data(json.utf8))
        #expect(payload.version == 1)
        #expect(payload.name == "mac-studio")
        #expect(payload.urls == [
            URL(string: "https://192.168.1.5:25444"),
            URL(string: "https://mac.tail1a2b.ts.net:25443"),
        ].compactMap(\.self))
        #expect(payload.token == "secret-abc")
        #expect(payload.fingerprint == "de7be0e6")
    }

    @Test("an unsupported version is rejected with PairError before any other field is read")
    func pairPayloadRejectsVersion() {
        let json = #"{"v":2,"name":"x","urls":["https://10.0.0.2:25444"],"token":"t","fp":"ab12"}"#
        #expect(throws: PairError.unsupportedVersion(2)) {
            try JSONDecoder().decode(PairPayload.self, from: Data(json.utf8))
        }
    }

    @Test("an IP-literal payload without fp is rejected: the LAN leg must pin")
    func pairPayloadRejectsIPWithoutFingerprint() {
        let json = #"{"v":1,"name":"lan-only","urls":["https://10.0.0.2:25444"],"token":"t"}"#
        #expect(throws: PairError.missingFingerprint) {
            try JSONDecoder().decode(PairPayload.self, from: Data(json.utf8))
        }
    }

    @Test("an IP-literal payload with an empty fp is rejected the same as a missing one")
    func pairPayloadRejectsIPWithEmptyFingerprint() {
        let json = #"{"v":1,"name":"lan-only","urls":["https://10.0.0.2:25444"],"token":"t","fp":""}"#
        #expect(throws: PairError.missingFingerprint) {
            try JSONDecoder().decode(PairPayload.self, from: Data(json.utf8))
        }
    }

    @Test("a hostname-only payload without fp decodes: that leg uses system trust")
    func pairPayloadHostnameNeedsNoFingerprint() throws {
        let json = #"{"v":1,"name":"tailnet-only","urls":["https://mac.tail1a2b.ts.net:25443"],"token":"t"}"#
        let payload = try JSONDecoder().decode(PairPayload.self, from: Data(json.utf8))
        #expect(payload.fingerprint == nil)
        #expect(payload.urls == [URL(string: "https://mac.tail1a2b.ts.net:25443")].compactMap(\.self))
    }

    @Test("a payload carrying a non-HTTPS URL is rejected outright")
    func pairPayloadRejectsInsecureURL() throws {
        let insecure = try #require(URL(string: "http://192.168.1.5:25444"))
        let json = #"{"v":1,"name":"mitm","urls":["http://192.168.1.5:25444"],"token":"t","fp":"ab12"}"#
        #expect(throws: PairError.insecureURL(insecure)) {
            try JSONDecoder().decode(PairPayload.self, from: Data(json.utf8))
        }
    }

    @Test("a payload missing the token fails to decode")
    func pairPayloadRejectsMissingToken() {
        let json = #"{"v":1,"name":"x","urls":["https://10.0.0.2:25444"]}"#
        #expect(throws: (any Error).self) {
            try JSONDecoder().decode(PairPayload.self, from: Data(json.utf8))
        }
    }

    @Test("malformed JSON fails to decode")
    func pairPayloadRejectsGarbage() {
        #expect(throws: (any Error).self) {
            try JSONDecoder().decode(PairPayload.self, from: Data("not json".utf8))
        }
    }

    @Test("a Machine built from a payload carries its urls and fingerprint")
    func machineFromPayload() throws {
        let payload = try PairPayload(
            version: 1,
            name: "studio",
            urls: [#require(URL(string: "https://10.0.0.3:25444"))],
            token: "t",
            fingerprint: "beef"
        )
        let machine = Machine(payload: payload)
        #expect(machine.name == "studio")
        #expect(machine.urls == payload.urls)
        #expect(machine.fingerprint == "beef")
    }

    @Test("the machine roster round-trips through an injected directory")
    func machineStoreRoundTrip() throws {
        let store = try temporaryStore()
        #expect(try store.load().isEmpty)

        let machines = try [
            Machine(
                id: "m1",
                name: "Laptop",
                urls: [#require(URL(string: "https://10.0.0.2:25444"))],
                fingerprint: "aa11"
            ),
            Machine(
                id: "m2",
                name: "Studio",
                urls: [
                    #require(URL(string: "https://10.0.0.3:25444")),
                    #require(URL(string: "https://studio.tail1a2b.ts.net:25443")),
                ],
                fingerprint: nil
            ),
        ]
        try store.save(machines)
        #expect(try store.load() == machines)
    }

    @Test("remove drops one machine and leaves the rest of the roster intact")
    func machineStoreRemove() throws {
        let store = try temporaryStore()
        let machines = try [
            Machine(id: "keep", name: "Keep", urls: [#require(URL(string: "https://10.0.0.2:25444"))]),
            Machine(id: "drop", name: "Drop", urls: [#require(URL(string: "https://10.0.0.3:25444"))]),
        ]
        try store.save(machines)

        try store.remove(id: "drop")
        let loaded = try store.load()
        #expect(loaded.map(\.id) == ["keep"])
    }

    private func temporaryStore() throws -> MachineStore {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("cc-runtime-tests-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return MachineStore(directory: directory)
    }
}
