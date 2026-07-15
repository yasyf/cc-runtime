// Small pure formatting helpers shared across the screens: the scope label a
// session row shows, a relative timestamp, and the diff line classing the question
// card colors. They are free functions so the rules are unit-testable directly.

import Foundation

/// scopeLabel trims a session's scope path to its last segment — the working
/// directory name a human recognizes — falling back to the raw scope.
func scopeLabel(_ scope: String) -> String {
    let trimmed = scope.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    let base = trimmed.split(separator: "/").last.map(String.init)
    return base ?? (scope.isEmpty ? "session" : scope)
}

/// DiffLineKind classifies one unified-diff line for coloring.
enum DiffLineKind: Equatable {
    case addition
    case deletion
    case hunk
    case context
}

/// diffLineKind classes a diff line the way the web QuestionCard does: an added or
/// removed line, a hunk header, or plain context.
func diffLineKind(_ line: String) -> DiffLineKind {
    if line.hasPrefix("+"), !line.hasPrefix("+++") {
        return .addition
    }
    if line.hasPrefix("-"), !line.hasPrefix("---") {
        return .deletion
    }
    if line.hasPrefix("@@") {
        return .hunk
    }
    return .context
}
