// The machine's live SSE fan-in. Each active subject has its own event stream
// (?session= is required), so a machine-wide feed means streaming every subject at
// once. The hub opens one stream per subject, gates on each stream's caught-up marker
// to drop the from-zero replay, then merges live notifications into the feed and
// refreshes the roster on question activity. It restarts whenever the subject set
// changes, so a poll that only moves a status or count never churns the streams.

import CcRuntimeKit
import Foundation

/// StreamHub streams a machine's active subjects while its screen is on. `run` owns
/// the streams for the duration of the call; cancelling it (a subject-set change or
/// the screen leaving) tears them all down.
@MainActor
@Observable
final class StreamHub {
    private let connection: MachineConnection
    private let feed: FeedStore
    private let sessions: SessionsModel

    init(connection: MachineConnection, feed: FeedStore, sessions: SessionsModel) {
        self.connection = connection
        self.feed = feed
        self.sessions = sessions
    }

    /// run opens a stream per subject and returns only when cancelled.
    func run(subjects: [String]) async {
        guard !subjects.isEmpty else {
            return
        }
        await withTaskGroup(of: Void.self) { group in
            for subject in subjects {
                group.addTask { [weak self] in
                    await self?.stream(subject)
                }
            }
        }
    }

    private func stream(_ subject: String) async {
        guard let client = connection.makeSSEClient(subject: subject) else {
            return
        }
        let connection = await client.connect()
        var caughtUp = false
        for await message in connection.messages {
            switch message {
            case .caughtUp:
                caughtUp = true
            case let .frame(event):
                guard caughtUp, let activity = classifyFrame(event) else {
                    continue
                }
                switch activity {
                case let .notification(payload):
                    feed.append(payload, subject: subject)
                case .questionsChanged:
                    await sessions.refresh()
                }
            }
        }
    }
}
