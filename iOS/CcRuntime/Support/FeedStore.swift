// The machine-level notification feed. There is no get-notifications endpoint, so
// the feed is built live: each subject's SSE stream and each arriving APNs push
// append here. It is machine-scoped state a screen owns, cleared or acked locally.

import CcRuntimeKit
import Foundation

/// NotificationEntry is one received notification: the subject it came from (nil for
/// a push whose subject is unknown), the message, its urgency, and when it landed.
struct NotificationEntry: Identifiable, Equatable, Sendable {
    let id: UUID
    let subject: String?
    let message: String
    let urgency: String?
    let date: Date

    init(id: UUID = UUID(), subject: String?, message: String, urgency: String?, date: Date = .now) {
        self.id = id
        self.subject = subject
        self.message = message
        self.urgency = urgency
        self.date = date
    }

    /// isUrgent reports the high-urgency styling flag.
    var isUrgent: Bool {
        urgency == "high"
    }
}

/// FeedStore accumulates a machine's notifications newest-last and exposes the local
/// ack and clear the feed screen drives. It holds no network of its own; the stream
/// hub and the push center feed it.
@MainActor
@Observable
final class FeedStore {
    private(set) var entries: [NotificationEntry] = []

    /// append records a decoded notification payload from `subject`'s stream.
    func append(_ payload: NotificationPayload, subject: String?, date: Date = .now) {
        entries.append(
            NotificationEntry(subject: subject, message: payload.message, urgency: payload.urgency, date: date)
        )
    }

    /// record appends a notification assembled from a push's title/body (the alert
    /// carries no structured message, so the body is the message).
    func record(message: String, urgency: String?, subject: String?, date: Date = .now) {
        entries.append(NotificationEntry(subject: subject, message: message, urgency: urgency, date: date))
    }

    /// ack drops one entry the human dismissed.
    func ack(_ id: UUID) {
        entries.removeAll { $0.id == id }
    }

    /// clear empties the feed.
    func clear() {
        entries.removeAll()
    }
}
