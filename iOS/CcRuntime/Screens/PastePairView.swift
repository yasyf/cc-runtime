import CcRuntimeKit
import SwiftUI

/// PastePairView is the manual add path: paste the JSON payload `cc-runtime pair`
/// prints (the same one the QR encodes), see it parsed into a preview, and pair. A
/// full payload — its ordered URLs and the LAN-leg fingerprint — can't be retyped
/// field-by-field, so pasting the whole blob is the hand-entry form.
struct PastePairView: View {
    let pairing: PairingModel

    @State private var text = ""

    private var decoded: Result<PairPayload, PairingModel.PairFormError> {
        PairingModel.decode(text)
    }

    var body: some View {
        Form {
            Section("Pairing Payload") {
                TextField("Paste the JSON from cc-runtime pair…", text: $text, axis: .vertical)
                    .lineLimit(3 ... 8)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .font(.caption.monospaced())
            }

            if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                switch decoded {
                case let .success(payload):
                    preview(payload)
                case let .failure(error):
                    Section {
                        Label(error.message, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                    }
                }
            }

            if case let .failed(message) = pairing.phase {
                Section {
                    Label(message, systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.red)
                }
            }

            Section {
                Button("Pair") {
                    if case let .success(payload) = decoded {
                        pairing.pair(payload: payload)
                    }
                }
                .disabled(!canSubmit)
            }
        }
        .navigationTitle("Paste Payload")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func preview(_ payload: PairPayload) -> some View {
        Section("Preview") {
            LabeledContent("Name", value: payload.name)
            ForEach(payload.urls, id: \.self) { url in
                LabeledContent("Address", value: url.absoluteString)
                    .font(.caption)
            }
            LabeledContent("LAN pinning", value: payload.fingerprint == nil ? "none" : "pinned")
        }
    }

    private var canSubmit: Bool {
        if case .success = decoded {
            return true
        }
        return false
    }
}
