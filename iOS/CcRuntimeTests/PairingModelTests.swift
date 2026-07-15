@testable import CcRuntimeApp
import CcRuntimeKit
import Foundation
import Testing

private struct FakeError: Error {}

private let goodPayload = #"""
{"v":1,"name":"studio","urls":["https://192.168.1.5:25444","https://host.ts.net:25443"],"token":"secret","fp":"abcd"}
"""#

@MainActor
@Test func pairScannedPersistsMachineAndToken() throws {
    let registry = InMemoryMachineRegistry()
    let model = PairingModel(registry: registry)

    model.pair(scanned: goodPayload)

    let machine = try requirePaired(model)
    #expect(machine.name == "studio")
    #expect(machine.urls.map(\.absoluteString) == [
        "https://192.168.1.5:25444",
        "https://host.ts.net:25443",
    ])
    #expect(machine.fingerprint == "abcd")
    #expect(registry.tokens[machine.id] == "secret")
    #expect(registry.machines.count == 1)
}

@MainActor
@Test func pairPayloadWithoutFingerprintPairs() throws {
    let registry = InMemoryMachineRegistry()
    let model = PairingModel(registry: registry)
    let url = try #require(URL(string: "https://host.ts.net:25443"))

    model.pair(payload: PairPayload(version: 1, name: "tailnet-only", urls: [url], token: "tok"))

    let machine = try requirePaired(model)
    #expect(machine.fingerprint == nil)
    #expect(registry.tokens[machine.id] == "tok")
}

@Test func decodeRejectsGarbage() {
    #expect(isFailure(PairingModel.decode("not a payload")))
    #expect(isFailure(PairingModel.decode("   ")))
}

@Test func decodeRejectsUnsupportedVersion() {
    let result = PairingModel.decode(#"{"v":2,"name":"x","urls":["https://10.0.0.2:25444"],"token":"t"}"#)
    guard case let .failure(error) = result else {
        Issue.record("expected failure")
        return
    }
    #expect(error.message.contains("version 2"))
}

@Test func decodeRejectsEmptyURLList() {
    let result = PairingModel.decode(#"{"v":1,"name":"x","urls":[],"token":"t"}"#)
    #expect(isFailure(result))
}

@Test func decodeAcceptsGoodPayload() {
    let result = PairingModel.decode(goodPayload)
    guard case let .success(payload) = result else {
        Issue.record("expected success")
        return
    }
    #expect(payload.name == "studio")
    #expect(payload.urls.count == 2)
    #expect(payload.fingerprint == "abcd")
}

@MainActor
@Test func commitFailurePropagates() {
    let registry = InMemoryMachineRegistry()
    registry.addError = FakeError()
    let model = PairingModel(registry: registry)

    model.pair(scanned: goodPayload)

    guard case .failed = model.phase else {
        Issue.record("expected failed, got \(model.phase)")
        return
    }
    #expect(registry.machines.isEmpty)
}

@MainActor
@Test func resetReturnsToIdle() {
    let model = PairingModel(registry: InMemoryMachineRegistry())
    model.pair(scanned: "garbage")

    model.reset()

    #expect(model.phase == .idle)
}

private func isFailure(_ result: Result<PairPayload, PairingModel.PairFormError>) -> Bool {
    if case .failure = result {
        return true
    }
    return false
}

@MainActor
private func requirePaired(_ model: PairingModel) throws -> Machine {
    guard case let .paired(machine) = model.phase else {
        Issue.record("expected paired, got \(model.phase)")
        throw FakeError()
    }
    return machine
}
