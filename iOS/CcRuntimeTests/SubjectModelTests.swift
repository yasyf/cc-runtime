@testable import CcRuntimeApp
import CcRuntimeKit
import Foundation
import Testing

private struct FakeError: Error {}

@MainActor
@Test func refreshLoadsOpenQuestions() async {
    let model = SubjectModel(subject: "s", client: FakeQuestions(questions: [
        makeQuestion(id: 1, prompt: "one"),
        makeQuestion(id: 2, prompt: "two"),
    ]))

    await model.refresh()

    #expect(model.phase == .loaded)
    #expect(model.questions.map(\.questionID) == [1, 2])
    #expect(!model.idled)
}

@MainActor
@Test func submitOptimisticallyRemovesAndRecordsIdled() async {
    let fake = FakeQuestions(questions: [makeQuestion(id: 1, prompt: "only")], idled: true)
    let model = SubjectModel(subject: "s", client: fake)
    await model.refresh()

    var draft = AnswerDraft()
    draft.other = "done"
    await model.submit(model.questions[0], draft: draft)

    #expect(model.questions.isEmpty)
    #expect(model.idled)
    #expect(fake.answered.count == 1)
    #expect(fake.answered[0].other == "done")
}

@MainActor
@Test func submitFailureReloadsAndSurfacesError() async {
    let fake = FakeQuestions(questions: [makeQuestion(id: 1, prompt: "only")])
    fake.answerError = APIError.status(code: 404, body: "unknown question")
    let model = SubjectModel(subject: "s", client: fake)
    await model.refresh()

    var draft = AnswerDraft()
    draft.selected = ["yes"]
    await model.submit(model.questions[0], draft: draft)

    #expect(model.submitError?.contains("404") == true)
    // The reconcile reload restores the question the fake still holds.
    #expect(model.questions.map(\.questionID) == [1])
}

@MainActor
@Test func staleRefreshCannotResurrectAnAnsweredQuestion() async {
    let fake = FakeQuestions(questions: [makeQuestion(id: 1, prompt: "only")], idled: true)
    let model = SubjectModel(subject: "s", client: fake)
    await model.refresh()

    // A poll snapshots [1] before the answer lands, then stalls in flight.
    fake.holdNextOpen = true
    let stale = Task { await model.refresh() }
    while !fake.holding {
        await Task.yield()
    }

    var draft = AnswerDraft()
    draft.selected = ["x"]
    await model.submit(model.questions[0], draft: draft)
    #expect(model.questions.isEmpty)
    #expect(model.idled)

    fake.releaseHeld()
    await stale.value

    // The stale snapshot is discarded: the answered question stays gone and
    // the idled banner survives.
    #expect(model.questions.isEmpty)
    #expect(model.idled)
}

@MainActor
@Test func submitDoesNotIdleWhileOtherQuestionsRemain() async {
    let fake = FakeQuestions(
        questions: [makeQuestion(id: 1, prompt: "a"), makeQuestion(id: 2, prompt: "b")],
        idled: false
    )
    let model = SubjectModel(subject: "s", client: fake)
    await model.refresh()

    var draft = AnswerDraft()
    draft.selected = ["x"]
    await model.submit(model.questions[0], draft: draft)

    #expect(model.questions.map(\.questionID) == [2])
    #expect(!model.idled)
}
