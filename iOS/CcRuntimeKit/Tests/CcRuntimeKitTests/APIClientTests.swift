@testable import CcRuntimeKit
import Foundation
import Testing

@Suite("API client")
struct APIClientTests {
    @Test("sessions decodes the subjects roster, mapping snake_case fields")
    func sessionsDecodes() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }
        StubURLProtocol.register(host: host) { _ in
            .json(200, #"{"subjects":[{"subject_id":"s1","scope":"repo:acme","status":"awaiting","pending":2}]}"#)
        }

        let client = try makeClient(host)
        let sessions = try await client.sessions()
        #expect(sessions == [Session(subjectID: "s1", scope: "repo:acme", status: "awaiting", pending: 2)])
    }

    @Test("pending decodes the open questions and parses the string-encoded payload")
    func pendingParsesPayload() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }

        let inner = #"{"header":"Deploy?","multiSelect":false,"options":[{"label":"Yes","description":"do it"},{"label":"No"}],"prompt":"Ship?"}"#
        let encoded = try String(decoding: JSONEncoder().encode(inner), as: UTF8.self)
        let reply = #"{"questions":[{"question_id":7,"header":"Deploy?","payload":\#(encoded)}]}"#
        StubURLProtocol.register(host: host) { _ in .json(200, reply) }

        let client = try makeClient(host)
        let pending = try await client.pending(subject: "s1")
        let question = try #require(pending.first)
        #expect(question.questionID == 7)
        #expect(question.header == "Deploy?")

        let parsed = try question.question()
        #expect(parsed.header == "Deploy?")
        #expect(parsed.prompt == "Ship?")
        #expect(parsed.multiSelect == false)
        #expect(parsed.options == [
            Option(label: "Yes", description: "do it"),
            Option(label: "No"),
        ])
    }

    @Test("openQuestions returns pending questions with their payloads already parsed")
    func openQuestionsParsed() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }

        let inner = #"{"options":[{"label":"Only"}],"prompt":"Go?"}"#
        let encoded = try String(decoding: JSONEncoder().encode(inner), as: UTF8.self)
        StubURLProtocol.register(host: host) { _ in
            .json(200, #"{"questions":[{"question_id":3,"payload":\#(encoded)}]}"#)
        }

        let open = try await makeClient(host).openQuestions(subject: "s1")
        #expect(open == [OpenQuestion(questionID: 3, question: QuestionPayload(options: [Option(label: "Only")], prompt: "Go?"))])
    }

    @Test("a no-option question (Go's nil slice, serialized as options null) decodes alongside an optioned one")
    func optionlessQuestionDecodes() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }

        let optionless = #"{"prompt":"Anything to add?","options":null}"#
        let optioned = #"{"options":[{"label":"Only"}],"prompt":"Go?"}"#
        let encodedOptionless = try String(decoding: JSONEncoder().encode(optionless), as: UTF8.self)
        let encodedOptioned = try String(decoding: JSONEncoder().encode(optioned), as: UTF8.self)
        StubURLProtocol.register(host: host) { _ in
            .json(200, #"{"questions":[{"question_id":1,"payload":\#(encodedOptionless)},{"question_id":2,"payload":\#(encodedOptioned)}]}"#)
        }

        let open = try await makeClient(host).openQuestions(subject: "s1")
        #expect(open == [
            OpenQuestion(questionID: 1, question: QuestionPayload(options: [], prompt: "Anything to add?")),
            OpenQuestion(questionID: 2, question: QuestionPayload(options: [Option(label: "Only")], prompt: "Go?")),
        ])
    }

    @Test("answer POSTs the body sans subject_id and returns whether the subject idled")
    func answerPostsBody() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }
        let recorder = RequestRecorder()
        StubURLProtocol.register(host: host) { request in
            recorder.record(request)
            return .json(200, #"{"idled":true}"#)
        }

        let client = try makeClient(host)
        let idled = try await client.answer(subject: "s1", AnswerPayload(questionID: 7, selected: ["Yes"], notes: "later"))
        #expect(idled)

        let request = try #require(recorder.requests.first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/api/subjects/s1/answer")
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer tok")

        let body = try #require(request.bodyData)
        let object = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(object["question_id"] as? Int == 7)
        #expect(object["selected"] as? [String] == ["Yes"])
        #expect(object["notes"] as? String == "later")
        // subject_id lives in the path, and a nil `other` is omitted, not sent null.
        #expect(object["subject_id"] == nil)
        #expect(object["other"] == nil)
    }

    @Test("a non-2xx reply surfaces as APIError.status carrying the code and body")
    func nonSuccessThrows() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }
        StubURLProtocol.register(host: host) { _ in .json(404, "unknown subject: s1") }

        let client = try makeClient(host)
        await #expect(throws: APIError.status(code: 404, body: "unknown subject: s1")) {
            try await client.pending(subject: "s1")
        }
    }

    private func makeClient(_ host: String) throws -> APIClient {
        try APIClient(
            baseURL: #require(URL(string: "https://\(host):25444")),
            bearerToken: "tok",
            urlSession: StubURLProtocol.session()
        )
    }
}
