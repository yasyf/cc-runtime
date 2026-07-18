import CcRuntimeKit
import Foundation
import SwiftUI

/// PairingModel is the shared pairing state machine every add-machine path drives: a
/// scanned QR payload or a pasted JSON payload both decode to a PairPayload and
/// converge on `commit`, which persists the machine through the registry. The phase
/// advances idle → paired on success or idle → failed with a display reason; `reset`
/// returns it to idle when a sheet is dismissed or a scan is retried.
@MainActor
@Observable
final class PairingModel {
    /// Phase is the outcome of the current pairing attempt.
    enum Phase: Equatable {
        case idle
        case paired(Machine)
        case failed(String)
    }

    /// PairFormError carries a decode failure's display reason.
    struct PairFormError: Error, Equatable {
        let message: String
    }

    private(set) var phase: Phase = .idle

    private let registry: any MachineRegistry

    init(registry: any MachineRegistry) {
        self.registry = registry
    }

    /// reset clears the phase back to idle.
    func reset() {
        phase = .idle
    }

    /// pair(scanned:) decodes a scanned QR string and pairs it, mapping a wrong
    /// version or unparseable payload to a failure reason.
    func pair(scanned string: String) {
        switch PairingModel.decode(string) {
        case let .success(payload): pair(payload: payload)
        case let .failure(error): phase = .failed(error.message)
        }
    }

    /// pair(payload:) persists a decoded handshake, naming the machine from its
    /// payload.
    func pair(payload: PairPayload) {
        do {
            let machine = Machine(payload: payload)
            try registry.add(machine, token: payload.token)
            phase = .paired(machine)
        } catch {
            phase = .failed("Couldn't save this machine.")
        }
    }

    /// decode parses a pairing payload string into a PairPayload, mapping an
    /// unsupported version, an empty URL list, or an unparseable string to a display
    /// reason. It is pure so the paste form previews a payload before committing and
    /// the scan path reuses the same rules.
    nonisolated static func decode(_ string: String) -> Result<PairPayload, PairFormError> {
        let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            return .failure(PairFormError(message: "Paste the pairing payload from cc-runtime pair."))
        }
        do {
            let payload = try JSONDecoder().decode(PairPayload.self, from: Data(trimmed.utf8))
            guard !payload.urls.isEmpty else {
                return .failure(PairFormError(message: "This pairing payload has no addresses."))
            }
            return .success(payload)
        } catch let PairError.unsupportedVersion(version) {
            let message = "This pairing code is version \(version); update the app to pair with it."
            return .failure(PairFormError(message: message))
        } catch PairError.insecureURL {
            return .failure(PairFormError(message: "This pairing payload has a non-HTTPS address; refusing to pair with it."))
        } catch PairError.missingFingerprint {
            return .failure(PairFormError(message: "This pairing payload is missing its LAN certificate fingerprint; re-run cc-runtime pair."))
        } catch {
            return .failure(PairFormError(message: "That isn't a cc-runtime pairing payload."))
        }
    }
}
