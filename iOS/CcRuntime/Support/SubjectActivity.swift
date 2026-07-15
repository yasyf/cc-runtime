// The mapping from a raw SSE frame to the two things a machine screen reacts to: a
// notification to append, or a question-set change that invalidates the roster and a
// subject's pending list. Split from the streaming code so the classification is
// tested against decoded Event frames without a socket.

import CcRuntimeKit

/// SubjectActivity is what one live SSE frame means to the UI.
enum SubjectActivity: Equatable {
    case notification(NotificationPayload)
    case questionsChanged
}

/// classifyFrame maps a decoded Event to the activity it drives, or nil for a frame
/// the UI ignores (channel presence, an unknown type). It mirrors the web stream's
/// onEvent: notification frames build the feed; question/answer frames invalidate the
/// pending and sessions snapshots.
func classifyFrame(_ event: Event) -> SubjectActivity? {
    switch event.type {
    case "interaction.notification":
        guard let payload = try? event.decode(NotificationPayload.self) else {
            return nil
        }
        return .notification(payload)
    case "interaction.question", "interaction.answer":
        return .questionsChanged
    default:
        return nil
    }
}
