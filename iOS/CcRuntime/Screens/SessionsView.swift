import CcRuntimeKit
import SwiftUI

/// SessionGroup is one status bucket of the roster — the awaiting sessions, the idle
/// ones — already ordered for display.
struct SessionGroup: Identifiable, Equatable {
    let status: String
    let sessions: [Session]

    var id: String {
        status
    }

    /// title is the section header: a known status title-cased, an unknown status as
    /// given.
    var title: String {
        switch status {
        case "awaiting": "Awaiting"
        case "idle": "Idle"
        default: status.capitalized
        }
    }
}

/// SessionsModel loads one machine's subject roster and groups it awaiting-first for
/// the list. It keeps the prior list visible across a reload so the rows never flash
/// empty, and maps a failure to a display string.
@MainActor
@Observable
final class SessionsModel {
    /// Phase is the load state the list renders.
    enum Phase: Equatable {
        case loading
        case loaded
        case empty
        case failed(String)
    }

    private(set) var phase: Phase = .loading
    private(set) var groups: [SessionGroup] = []

    private var client: (any SessionsProviding)?

    init(client: (any SessionsProviding)? = nil) {
        self.client = client
    }

    /// attach wires the resolved REST client once the connection is up.
    func attach(_ client: any SessionsProviding) {
        self.client = client
    }

    /// refresh fetches the roster and regroups it, holding the prior groups visible
    /// while a reload is in flight.
    func refresh() async {
        guard let client else {
            return
        }
        if groups.isEmpty {
            phase = .loading
        }
        do {
            let fetched = try await client.sessions()
            groups = SessionsModel.grouped(fetched)
            phase = groups.isEmpty ? .empty : .loaded
        } catch {
            phase = .failed(SessionsModel.message(for: error))
        }
    }

    /// subjectIDs is the stable set of subjects the stream hub subscribes to, sorted
    /// so a poll that changes only a status or count doesn't churn the streams.
    var subjectIDs: [String] {
        groups.flatMap { $0.sessions.map(\.subjectID) }.sorted()
    }

    /// grouped buckets sessions by status (awaiting first, then idle, then anything
    /// else alphabetically) and orders each bucket most-pending-first.
    nonisolated static func grouped(_ sessions: [Session]) -> [SessionGroup] {
        let buckets = Dictionary(grouping: sessions, by: \.status)
        return buckets.keys
            .sorted { lhs, rhs in
                let leftRank = rank(lhs)
                let rightRank = rank(rhs)
                return leftRank == rightRank ? lhs < rhs : leftRank < rightRank
            }
            .map { status in
                let ordered = (buckets[status] ?? []).sorted { lhs, rhs in
                    lhs.pending == rhs.pending ? lhs.subjectID < rhs.subjectID : lhs.pending > rhs.pending
                }
                return SessionGroup(status: status, sessions: ordered)
            }
    }

    private nonisolated static func rank(_ status: String) -> Int {
        switch status {
        case "awaiting": 0
        case "idle": 1
        default: 2
        }
    }

    private static func message(for error: Error) -> String {
        if case let APIError.status(code, _) = error {
            return "The machine returned an error (\(code))."
        }
        return "Couldn't reach this machine."
    }
}

/// SessionsView is the connected-machine surface: it resolves the endpoint, lists the
/// subject roster grouped awaiting-first, runs the per-subject stream hub that feeds
/// the notification feed and refreshes the roster live, and registers this device for
/// push. A toolbar bell opens the machine's notification feed; a tap drills into a
/// subject.
struct SessionsView: View {
    let machine: Machine

    @Environment(PushCenter.self) private var push

    @State private var connection: MachineConnection
    @State private var sessions = SessionsModel()
    @State private var feed = FeedStore()
    @State private var hub: StreamHub?
    @State private var connectAttempt = 0
    @State private var showingFeed = false
    @State private var deepLinkSubject: String?

    init(machine: Machine) {
        self.machine = machine
        let token = (try? TokenStore.token(machineID: machine.id)) ?? nil
        _connection = State(initialValue: MachineConnection(machine: machine, token: token))
    }

    init(machine: Machine, connection: MachineConnection) {
        self.machine = machine
        _connection = State(initialValue: connection)
    }

    var body: some View {
        content
            .navigationTitle(machine.name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { feedButton }
            .task(id: connectAttempt) { await start() }
            .sheet(isPresented: $showingFeed) {
                NavigationStack {
                    NotificationFeedView(feed: feed)
                }
            }
            .navigationDestination(item: $deepLinkSubject) { subject in
                SubjectView(connection: connection, feed: feed, subject: subject)
            }
            .onChange(of: push.pendingDeepLink) { _, link in
                routeDeepLink(link)
            }
            .onChange(of: sessions.subjectIDs) { _, _ in
                routeDeepLink(push.pendingDeepLink)
            }
            .onChange(of: push.deviceTokenHex) { _, _ in
                Task { await registerDeviceToken() }
            }
    }

    @ViewBuilder
    private var content: some View {
        switch connection.state {
        case .idle, .connecting:
            ProgressView("Connecting…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case .unreachable:
            ContentUnavailableView {
                Label("Can't Reach Machine", systemImage: "wifi.exclamationmark")
            } description: {
                Text(
                    "None of this machine's addresses answered. Check that the daemon is "
                        + "running and you're on the same network."
                )
            } actions: {
                Button("Try Again") { connectAttempt += 1 }
            }
        case .connected:
            roster
        }
    }

    @ViewBuilder
    private var roster: some View {
        switch sessions.phase {
        case .loading:
            ProgressView("Loading sessions…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case .empty:
            ContentUnavailableView(
                "No Sessions",
                systemImage: "bubble.left.and.bubble.right",
                description: Text("This machine has no active sessions right now.")
            )
        case let .failed(message):
            ContentUnavailableView {
                Label("Can't Load Sessions", systemImage: "wifi.exclamationmark")
            } description: {
                Text(message)
            } actions: {
                Button("Try Again") { Task { await sessions.refresh() } }
            }
        case .loaded:
            list
        }
    }

    private var list: some View {
        List {
            ForEach(sessions.groups) { group in
                Section(group.title) {
                    ForEach(group.sessions) { session in
                        NavigationLink {
                            SubjectView(connection: connection, feed: feed, subject: session.subjectID)
                        } label: {
                            SessionRow(session: session)
                        }
                    }
                }
            }
        }
        .refreshable { await sessions.refresh() }
        .task(id: sessions.subjectIDs) { await hub?.run(subjects: sessions.subjectIDs) }
    }

    private var feedButton: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Button {
                showingFeed = true
            } label: {
                Label("Notifications", systemImage: feed.entries.isEmpty ? "bell" : "bell.badge")
            }
        }
    }

    private func start() async {
        await connection.connect()
        guard case .connected = connection.state, let client = connection.apiClient else {
            return
        }
        sessions.attach(client)
        hub = StreamHub(connection: connection, feed: feed, sessions: sessions)
        await push.requestAuthorization()
        await registerDeviceToken()
        await pollRoster()
    }

    /// pollRoster refreshes the roster on an interval so the list stays live even when
    /// no subject is open, mirroring the web client's polling cadence.
    private func pollRoster() async {
        while !Task.isCancelled {
            await sessions.refresh()
            try? await Task.sleep(for: .seconds(4))
        }
    }

    /// registerDeviceToken posts this device's APNs token to the connected machine.
    /// The token arrives asynchronously, so this runs both after authorization and
    /// again when the token lands.
    private func registerDeviceToken() async {
        guard connection.state == .connected,
              let hex = push.deviceTokenHex,
              let registrar = connection.registrar()
        else {
            return
        }
        try? await registrar.register(hexToken: hex)
    }

    /// routeDeepLink opens the pending subject once the roster holds it. It runs on
    /// link arrival and again on every roster change, so a link set before the
    /// roster loads (cold launch, mid-connect) routes as soon as its subject appears.
    private func routeDeepLink(_ link: PushCenter.DeepLink?) {
        guard let link, sessions.subjectIDs.contains(link.subject) else {
            return
        }
        deepLinkSubject = link.subject
        push.clearDeepLink()
    }
}

private struct SessionRow: View {
    let session: Session

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(scopeLabel(session.scope))
                    .font(.headline)
                Text(session.subjectID)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer()
            if session.pending > 0 {
                Text("\(session.pending)")
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(.tint, in: Capsule())
                    .foregroundStyle(.white)
            }
        }
        .padding(.vertical, 2)
    }
}
