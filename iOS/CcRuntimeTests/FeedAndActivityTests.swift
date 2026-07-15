@testable import CcRuntimeApp
import CcRuntimeKit
import Foundation
import Testing

@MainActor
@Test func feedAppendsAcksAndClears() {
    let feed = FeedStore()
    feed.append(NotificationPayload(message: "first", urgency: nil), subject: "s1")
    feed.append(NotificationPayload(message: "second", urgency: "high"), subject: "s1")

    #expect(feed.entries.map(\.message) == ["first", "second"])
    #expect(feed.entries[1].isUrgent)

    let firstID = feed.entries[0].id
    feed.ack(firstID)
    #expect(feed.entries.map(\.message) == ["second"])

    feed.clear()
    #expect(feed.entries.isEmpty)
}

@MainActor
@Test func feedRecordsPushNotice() {
    let feed = FeedStore()
    feed.record(message: "pushed", urgency: "high", subject: nil)
    #expect(feed.entries.count == 1)
    #expect(feed.entries[0].subject == nil)
    #expect(feed.entries[0].isUrgent)
}

@Test func classifyNotificationFrame() throws {
    let data = Data(#"{"type":"interaction.notification","message":"build done","urgency":"high"}"#.utf8)
    let event = try Event.wireFrame(data)

    guard case let .notification(payload) = classifyFrame(event) else {
        Issue.record("expected notification")
        return
    }
    #expect(payload.message == "build done")
    #expect(payload.urgency == "high")
}

@Test func classifyQuestionAndAnswerFramesAsChanged() throws {
    let question = try Event.wireFrame(Data(#"{"type":"interaction.question","options":[]}"#.utf8))
    let answer = try Event.wireFrame(Data(#"{"type":"interaction.answer","question_id":1,"selected":[]}"#.utf8))

    #expect(classifyFrame(question) == .questionsChanged)
    #expect(classifyFrame(answer) == .questionsChanged)
}

@Test func classifyIgnoresPresenceFrames() throws {
    let event = try Event.wireFrame(Data(#"{"type":"channel.changed","connected":true}"#.utf8))
    #expect(classifyFrame(event) == nil)
}

@Test func pushDeepLinkReadsPayloadSubject() {
    let link = PushCenter.deepLink(from: ["payload": ["subject": "abc", "type": "question"]])
    #expect(link == PushCenter.DeepLink(subject: "abc"))
}

@Test func pushDeepLinkFallsBackToThreadID() {
    let link = PushCenter.deepLink(from: ["aps": ["thread-id": "xyz"]])
    #expect(link == PushCenter.DeepLink(subject: "xyz"))
}

@Test func pushDeepLinkNilWhenAbsent() {
    #expect(PushCenter.deepLink(from: ["aps": ["alert": ["title": "hi"]]]) == nil)
}
