import CcRuntimeKit
import SwiftUI

/// Reachability is the machine-row status dot: unknown until the first probe
/// resolves, then reachable or unreachable.
enum Reachability: Sendable {
    case unknown
    case reachable
    case unreachable
    case repairRequired
}

/// ReachabilityProbe resolves whether a machine answers on any of its legs, given its
/// token. Production races the kit's EndpointProber; tests inject a scripted result.
typealias ReachabilityProbe = @Sendable (Machine, String) async -> Bool

/// MachinesModel owns the paired-machine roster and a lightweight reachability probe
/// per machine. The probe resolves the same candidate legs the connection uses, so a
/// green dot means the token is accepted and a leg is up.
@MainActor
@Observable
final class MachinesModel {
    private(set) var machines: [Machine] = []
    private(set) var reachability: [String: Reachability] = [:]
    private(set) var loadError: String?
    private(set) var tokens: [String: String] = [:]
    private(set) var tokenErrors: [String: String] = [:]

    private let registry: any MachineRegistry
    private let probe: ReachabilityProbe

    init(
        registry: any MachineRegistry = KeychainMachineRegistry(),
        probe: @escaping ReachabilityProbe = MachinesModel.defaultProbe
    ) {
        self.registry = registry
        self.probe = probe
    }

    static let defaultProbe: ReachabilityProbe = { machine, token in
        await EndpointProber(machine: machine, token: token).probe() != nil
    }

    /// load refreshes the roster from the registry.
    func load() {
        do {
            let loaded = try registry.load()
            var loadedTokens: [String: String] = [:]
            var loadedTokenErrors: [String: String] = [:]
            for machine in loaded {
                do {
                    loadedTokens[machine.id] = try registry.token(for: machine.id)
                } catch {
                    loadedTokenErrors[machine.id] = error.localizedDescription
                }
            }
            machines = loaded
            tokens = loadedTokens
            tokenErrors = loadedTokenErrors
            loadError = nil
        } catch {
            loadError = "Couldn't load your machines."
        }
    }

    /// forget drops a machine from the roster and deletes its Keychain token.
    func forget(_ machine: Machine) {
        do {
            try registry.remove(machine)
        } catch {
            loadError = "Couldn't forget this machine."
            return
        }
        reachability.removeValue(forKey: machine.id)
        load()
    }

    /// probeAll probes every machine concurrently.
    func probeAll() async {
        await withTaskGroup(of: Void.self) { group in
            for machine in machines {
                group.addTask { await self.probeOne(machine) }
            }
        }
    }

    /// probeOne resolves one machine's reachability after its exact token is loaded.
    func probeOne(_ machine: Machine) async {
        guard let token = tokens[machine.id] else {
            reachability[machine.id] = .repairRequired
            return
        }
        reachability[machine.id] = await probe(machine, token) ? .reachable : .unreachable
    }

    func reachability(of machine: Machine) -> Reachability {
        reachability[machine.id] ?? .unknown
    }

    func token(for machine: Machine) -> String? {
        tokens[machine.id]
    }

    func tokenError(for machine: Machine) -> String {
        tokenErrors[machine.id] ??
            "Transfer this machine's bearer token into the v1 device-bound Keychain identity, or forget and re-pair it."
    }
}

/// AddRoute is the add-machine entry the roster's menu opens as a sheet.
private enum AddRoute: String, Identifiable {
    case scan
    case browse
    case paste

    var id: String {
        rawValue
    }
}

/// MachinesView is the app root: the roster of paired machines with a reachability
/// dot, an add-machine menu, and swipe-to-forget. A tap drills into the machine's
/// sessions.
struct MachinesView: View {
    @State private var model: MachinesModel
    @State private var pairing: PairingModel
    @State private var addRoute: AddRoute?

    init(
        registry: some MachineRegistry = KeychainMachineRegistry(),
        probe: @escaping ReachabilityProbe = MachinesModel.defaultProbe
    ) {
        _model = State(initialValue: MachinesModel(registry: registry, probe: probe))
        _pairing = State(initialValue: PairingModel(registry: registry))
    }

    var body: some View {
        NavigationStack {
            roster
                .navigationTitle("Machines")
                .toolbar { addMenu }
                .task {
                    model.load()
                    await model.probeAll()
                }
                .refreshable { await model.probeAll() }
        }
        .sheet(item: $addRoute) { route in
            addSheet(route)
        }
    }

    @ViewBuilder
    private var roster: some View {
        if model.machines.isEmpty {
            ContentUnavailableView {
                Label("No Machines", systemImage: "desktopcomputer")
            } description: {
                Text("Pair a machine running cc-runtime to answer its questions and receive its notifications.")
            } actions: {
                Button("Add Machine") { addRoute = .scan }
            }
        } else {
            List {
                ForEach(model.machines) { machine in
                    NavigationLink {
                        if let token = model.token(for: machine) {
                            SessionsView(
                                machine: machine,
                                connection: MachineConnection(machine: machine, token: token)
                            )
                        } else {
                            TokenRepairView(machine: machine, message: model.tokenError(for: machine))
                        }
                    } label: {
                        MachineRow(machine: machine, reachability: model.reachability(of: machine))
                    }
                    .swipeActions(edge: .trailing) {
                        Button(role: .destructive) {
                            model.forget(machine)
                        } label: {
                            Label("Forget", systemImage: "trash")
                        }
                    }
                }
            }
        }
    }

    private var addMenu: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Menu {
                Button { addRoute = .scan } label: { Label("Scan QR Code", systemImage: "qrcode.viewfinder") }
                Button { addRoute = .browse } label: { Label("Browse Network", systemImage: "network") }
                Button { addRoute = .paste } label: { Label("Paste Payload", systemImage: "doc.on.clipboard") }
            } label: {
                Label("Add Machine", systemImage: "plus")
            }
        }
    }

    private func addSheet(_ route: AddRoute) -> some View {
        NavigationStack {
            Group {
                switch route {
                case .scan: PairScannerView(pairing: pairing)
                case .browse: BonjourBrowserView(pairing: pairing)
                case .paste: PastePairView(pairing: pairing)
                }
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismissAdd() }
                }
            }
        }
        .onChange(of: pairing.phase) { _, phase in
            if case .paired = phase {
                addRoute = nil
                pairing.reset()
                model.load()
                Task { await model.probeAll() }
            }
        }
    }

    private func dismissAdd() {
        addRoute = nil
        pairing.reset()
    }
}

private struct MachineRow: View {
    let machine: Machine
    let reachability: Reachability

    var body: some View {
        HStack(spacing: 12) {
            Circle()
                .fill(dotColor)
                .frame(width: 10, height: 10)
            VStack(alignment: .leading, spacing: 2) {
                Text(machine.name)
                    .font(.headline)
                Text(addressSummary)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
        }
        .padding(.vertical, 2)
    }

    private var addressSummary: String {
        if case .repairRequired = reachability {
            return "Transfer token or forget and re-pair"
        }
        guard let first = machine.urls.first else {
            return "no address"
        }
        let host = first.host() ?? first.absoluteString
        let extra = machine.urls.count - 1
        return extra > 0 ? "\(host) +\(extra)" : host
    }

    private var dotColor: Color {
        switch reachability {
        case .unknown: .secondary
        case .reachable: .green
        case .unreachable: .red
        case .repairRequired: .orange
        }
    }
}

private struct TokenRepairView: View {
    let machine: Machine
    let message: String

    var body: some View {
        ContentUnavailableView {
            Label("Token Transfer Required", systemImage: "key.slash")
        } description: {
            Text(message)
        }
        .navigationTitle(machine.name)
        .navigationBarTitleDisplayMode(.inline)
    }
}
