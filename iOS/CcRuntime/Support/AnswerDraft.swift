// The in-progress answer a question card edits before submitting: the picked option
// labels plus the two free-text fields. Split out as a value type so the pick rules
// (single-select replaces, multi-select toggles) and the payload it builds are
// exercised in isolation.

import CcRuntimeKit
import Foundation

/// AnswerDraft holds a question card's editable answer. `toggle` applies the
/// single- or multi-select rule; `payload` builds the wire body, dropping blank
/// free-text so an empty field never rides as `""`.
struct AnswerDraft: Equatable {
    var selected: [String] = []
    var other: String = ""
    var notes: String = ""

    /// toggle picks or unpicks an option: multi-select adds or removes it; single-
    /// select replaces the selection, and re-picking the current choice clears it.
    mutating func toggle(_ label: String, multiSelect: Bool) {
        if multiSelect {
            if let index = selected.firstIndex(of: label) {
                selected.remove(at: index)
            } else {
                selected.append(label)
            }
        } else {
            selected = selected == [label] ? [] : [label]
        }
    }

    /// isFilled reports whether there is anything to submit: a picked option or non-
    /// blank free text.
    var isFilled: Bool {
        !selected.isEmpty
            || !other.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            || !notes.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// payload builds the AnswerPayload for `questionID`, trimming free text and
    /// omitting it entirely when blank.
    func payload(questionID: Int64) -> AnswerPayload {
        let trimmedOther = other.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedNotes = notes.trimmingCharacters(in: .whitespacesAndNewlines)
        return AnswerPayload(
            questionID: questionID,
            selected: selected,
            other: trimmedOther.isEmpty ? nil : trimmedOther,
            notes: trimmedNotes.isEmpty ? nil : trimmedNotes
        )
    }
}
