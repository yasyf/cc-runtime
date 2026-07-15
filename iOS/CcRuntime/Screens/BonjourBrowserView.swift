import CcRuntimeKit
import Network
import SwiftUI
import UIKit

/// DiscoveredMachine is one Bonjour result: the advertised service name and, once
/// resolved, the `host:port` it lives at.
struct DiscoveredMachine: Identifiable, Hashable, Sendable {
    let id: String
    let name: String
    var address: String?
}

/// BonjourModel browses for `_cc-runtime._tcp` advertisers and resolves each to a
/// host and port. The first browse triggers the local-network permission prompt; a
/// denial surfaces as a silent empty result, which the view explains with a Settings
/// deep link rather than a hard error.
@MainActor
@Observable
final class BonjourModel {
    private(set) var services: [DiscoveredMachine] = []

    private var browser: NWBrowser?
    private let queue = DispatchQueue(label: "com.yasyf.cc-runtime.bonjour")

    /// start begins browsing; calling it again while a browse is live is a no-op.
    func start() {
        guard browser == nil else { return }
        let descriptor = NWBrowser.Descriptor.bonjour(type: "_cc-runtime._tcp", domain: nil)
        let browser = NWBrowser(for: descriptor, using: NWParameters())
        browser.browseResultsChangedHandler = { [weak self] results, _ in
            self?.ingest(results)
        }
        browser.start(queue: queue)
        self.browser = browser
    }

    /// stop tears the browse down and clears discovered rows.
    func stop() {
        browser?.cancel()
        browser = nil
        services = []
    }

    private nonisolated func ingest(_ results: Set<NWBrowser.Result>) {
        var names: [String] = []
        for result in results {
            guard case let .service(name, _, _, _) = result.endpoint else { continue }
            names.append(name)
            resolve(result.endpoint, name: name)
        }
        let discovered = names.sorted()
        Task { @MainActor [weak self] in self?.merge(names: discovered) }
    }

    private nonisolated func resolve(_ endpoint: NWEndpoint, name: String) {
        let box = ConnectionBox(NWConnection(to: endpoint, using: .tcp))
        box.connection.stateUpdateHandler = { [weak self] state in
            switch state {
            case .ready:
                if let address = Self.resolvedAddress(from: box.connection.currentPath?.remoteEndpoint) {
                    Task { @MainActor [weak self] in self?.setAddress(name: name, address: address) }
                }
                box.connection.cancel()
            case .failed, .cancelled:
                box.connection.stateUpdateHandler = nil
            default:
                break
            }
        }
        box.connection.start(queue: queue)
    }

    private nonisolated static func resolvedAddress(from endpoint: NWEndpoint?) -> String? {
        guard case let .hostPort(host, port)? = endpoint else { return nil }
        return "\(hostString(host)):\(port.rawValue)"
    }

    private func merge(names: [String]) {
        services = names.map { name in
            services.first { $0.id == name } ?? DiscoveredMachine(id: name, name: name, address: nil)
        }
    }

    private func setAddress(name: String, address: String) {
        guard let index = services.firstIndex(where: { $0.id == name }) else { return }
        services[index].address = address
    }

    private nonisolated static func hostString(_ host: NWEndpoint.Host) -> String {
        switch host {
        case let .name(name, _): name
        case let .ipv4(address): "\(address)"
        case let .ipv6(address): "[\(address)]"
        @unknown default: ""
        }
    }
}

/// ConnectionBox carries an NWConnection into the resolver's Sendable state handler.
/// The connection is only ever touched on the browse queue, so the unchecked
/// conformance is sound.
private final class ConnectionBox: @unchecked Sendable {
    let connection: NWConnection

    init(_ connection: NWConnection) {
        self.connection = connection
    }
}

/// BonjourBrowserView lists machines discovered on the local network. Pairing needs
/// the signed payload the QR carries — its ordered URLs and the LAN fingerprint —
/// so a discovered row can't pair on its own: tapping it opens the scanner. Discovery
/// is the confirmation that the machine is on the network, not the credential.
struct BonjourBrowserView: View {
    let pairing: PairingModel

    @State private var model = BonjourModel()
    @Environment(\.openURL) private var openURL

    var body: some View {
        List {
            Section("Discovered") {
                if model.services.isEmpty {
                    Label("Searching…", systemImage: "antenna.radiowaves.left.and.right")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(model.services) { service in
                        NavigationLink {
                            PairScannerView(pairing: pairing)
                        } label: {
                            serviceLabel(service)
                        }
                    }
                }
            }
            Section {
                Button {
                    if let url = URL(string: UIApplication.openSettingsURLString) {
                        openURL(url)
                    }
                } label: {
                    Label("Open Settings", systemImage: "gear")
                }
            } header: {
                Text("Not Seeing Your Machine?")
            } footer: {
                Text(
                    "Discovery needs Local Network permission. If nothing appears, "
                        + "make sure it's allowed for cc-runtime in Settings."
                )
            }
        }
        .navigationTitle("Browse Network")
        .navigationBarTitleDisplayMode(.inline)
        .task { model.start() }
        .onDisappear { model.stop() }
    }

    private func serviceLabel(_ service: DiscoveredMachine) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(service.name)
                .font(.headline)
            Text(service.address ?? "Resolving…")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }
}
