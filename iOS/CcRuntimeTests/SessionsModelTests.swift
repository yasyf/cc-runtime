@testable import CcRuntimeApp
import CcRuntimeKit
import Foundation
import Testing

@Test func groupedOrdersAwaitingBeforeIdle() {
    let sessions = [
        makeSession(subject: "a", status: "idle"),
        makeSession(subject: "b", status: "awaiting", pending: 1),
    ]

    let groups = SessionsModel.grouped(sessions)

    #expect(groups.map(\.status) == ["awaiting", "idle"])
    #expect(groups.first?.title == "Awaiting")
}

@Test func groupedOrdersUnknownStatusLast() {
    let sessions = [
        makeSession(subject: "a", status: "wedged"),
        makeSession(subject: "b", status: "idle"),
        makeSession(subject: "c", status: "awaiting", pending: 2),
    ]

    let groups = SessionsModel.grouped(sessions)

    #expect(groups.map(\.status) == ["awaiting", "idle", "wedged"])
}

@Test func groupedSortsMostPendingFirstWithinGroup() {
    let sessions = [
        makeSession(subject: "low", status: "awaiting", pending: 1),
        makeSession(subject: "high", status: "awaiting", pending: 5),
        makeSession(subject: "mid", status: "awaiting", pending: 3),
    ]

    let groups = SessionsModel.grouped(sessions)

    #expect(groups.first?.sessions.map(\.subjectID) == ["high", "mid", "low"])
}

@MainActor
@Test func refreshLoadsAndGroups() async {
    let model = SessionsModel(client: FakeSessions(.roster([
        makeSession(subject: "a", status: "idle"),
        makeSession(subject: "b", status: "awaiting", pending: 1),
    ])))

    await model.refresh()

    #expect(model.phase == .loaded)
    #expect(model.groups.map(\.status) == ["awaiting", "idle"])
    #expect(model.subjectIDs == ["a", "b"])
}

@MainActor
@Test func refreshOnEmptyRosterReportsEmpty() async {
    let model = SessionsModel(client: FakeSessions(.roster([])))

    await model.refresh()

    #expect(model.phase == .empty)
    #expect(model.groups.isEmpty)
}

@MainActor
@Test func refreshFailureSurfacesStatusCode() async {
    let model = SessionsModel(client: FakeSessions(.failure(APIError.status(code: 503, body: "down"))))

    await model.refresh()

    guard case let .failed(message) = model.phase else {
        Issue.record("expected failed, got \(model.phase)")
        return
    }
    #expect(message.contains("503"))
}

@MainActor
@Test func refreshWithoutClientIsNoop() async {
    let model = SessionsModel()

    await model.refresh()

    #expect(model.phase == .loading)
    #expect(model.groups.isEmpty)
}
