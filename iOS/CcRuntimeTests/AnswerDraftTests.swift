@testable import CcRuntimeApp
import CcRuntimeKit
import Foundation
import Testing

@Test func singleSelectReplacesSelection() {
    var draft = AnswerDraft()
    draft.toggle("a", multiSelect: false)
    draft.toggle("b", multiSelect: false)
    #expect(draft.selected == ["b"])
}

@Test func singleSelectRepickClears() {
    var draft = AnswerDraft()
    draft.toggle("a", multiSelect: false)
    draft.toggle("a", multiSelect: false)
    #expect(draft.selected.isEmpty)
}

@Test func multiSelectTogglesEachIndependently() {
    var draft = AnswerDraft()
    draft.toggle("a", multiSelect: true)
    draft.toggle("b", multiSelect: true)
    draft.toggle("a", multiSelect: true)
    #expect(draft.selected == ["b"])
}

@Test func isFilledTracksSelectionAndText() {
    var draft = AnswerDraft()
    #expect(!draft.isFilled)
    draft.notes = "   "
    #expect(!draft.isFilled)
    draft.notes = "note"
    #expect(draft.isFilled)
}

@Test func payloadTrimsAndOmitsBlankFreeText() {
    var draft = AnswerDraft()
    draft.selected = ["yes"]
    draft.other = "  "
    draft.notes = "  careful  "

    let payload = draft.payload(questionID: 7)

    #expect(payload.questionID == 7)
    #expect(payload.selected == ["yes"])
    #expect(payload.other == nil)
    #expect(payload.notes == "careful")
}
