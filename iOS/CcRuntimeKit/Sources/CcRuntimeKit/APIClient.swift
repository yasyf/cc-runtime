// The REST edge of the cc-runtime client: a thin, Sendable wrapper over URLSession
// that speaks the daemon's HTTP plane (interaction/rest.go). It carries a base URL
// and an optional bearer token, mirrors the browser reference in web/src/events.ts,
// and stays free of app state. The wire field names match the Go JSON exactly.

import Foundation

// --- Domain wire payloads (mirror interaction/payload.go) ---

/// Option is one selectable choice in a question, mirroring Claude Code's native
/// AskUserQuestion tool so the mapping is 1:1.
public struct Option: Decodable, Equatable, Sendable {
    public let label: String
    public let description: String?
    public let preview: String?

    public init(label: String, description: String? = nil, preview: String? = nil) {
        self.label = label
        self.description = description
        self.preview = preview
    }
}

/// QuestionPayload is the structured ask carried on an interaction.question frame
/// and returned, string-encoded, in a subject's pending list.
public struct QuestionPayload: Decodable, Equatable, Sendable {
    public let header: String?
    public let multiSelect: Bool?
    public let options: [Option]
    public let prompt: String?
    public let reasoning: String?
    public let diff: String?

    public init(
        header: String? = nil,
        multiSelect: Bool? = nil,
        options: [Option],
        prompt: String? = nil,
        reasoning: String? = nil,
        diff: String? = nil
    ) {
        self.header = header
        self.multiSelect = multiSelect
        self.options = options
        self.prompt = prompt
        self.reasoning = reasoning
        self.diff = diff
    }

    /// Go serializes a no-option question's nil slice as `"options": null`, so a
    /// null or absent array decodes as empty rather than failing the whole batch.
    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        header = try container.decodeIfPresent(String.self, forKey: .header)
        multiSelect = try container.decodeIfPresent(Bool.self, forKey: .multiSelect)
        options = try container.decodeIfPresent([Option].self, forKey: .options) ?? []
        prompt = try container.decodeIfPresent(String.self, forKey: .prompt)
        reasoning = try container.decodeIfPresent(String.self, forKey: .reasoning)
        diff = try container.decodeIfPresent(String.self, forKey: .diff)
    }

    private enum CodingKeys: String, CodingKey {
        case header
        case multiSelect
        case options
        case prompt
        case reasoning
        case diff
    }
}

/// NotificationPayload is a one-way message carried on an interaction.notification
/// frame.
public struct NotificationPayload: Decodable, Equatable, Sendable {
    public let message: String
    public let urgency: String?

    public init(message: String, urgency: String? = nil) {
        self.message = message
        self.urgency = urgency
    }
}

// --- REST shapes (interaction/rest.go, interaction/handlers.go) ---

/// Session is one row of GET /api/sessions: a subject, its scope, lifecycle status,
/// and open-question count.
public struct Session: Decodable, Equatable, Sendable, Identifiable {
    public let subjectID: String
    public let scope: String
    public let status: String
    public let pending: Int

    public var id: String {
        subjectID
    }

    public init(subjectID: String, scope: String, status: String, pending: Int) {
        self.subjectID = subjectID
        self.scope = scope
        self.status = status
        self.pending = pending
    }

    private enum CodingKeys: String, CodingKey {
        case subjectID = "subject_id"
        case scope
        case status
        case pending
    }
}

/// PendingQuestion is one open question from GET /api/subjects/{id}/pending. Its
/// `payload` is the full QuestionPayload as an unparsed JSON string.
public struct PendingQuestion: Decodable, Equatable, Sendable, Identifiable {
    public let questionID: Int64
    public let header: String?
    public let payload: String

    public var id: Int64 {
        questionID
    }

    public init(questionID: Int64, header: String? = nil, payload: String) {
        self.questionID = questionID
        self.header = header
        self.payload = payload
    }

    private enum CodingKeys: String, CodingKey {
        case questionID = "question_id"
        case header
        case payload
    }

    /// question parses the string-encoded `payload` into its QuestionPayload.
    public func question() throws -> QuestionPayload {
        try JSONDecoder().decode(QuestionPayload.self, from: Data(payload.utf8))
    }
}

/// OpenQuestion is a PendingQuestion with its payload parsed for rendering.
public struct OpenQuestion: Equatable, Sendable, Identifiable {
    public let questionID: Int64
    public let question: QuestionPayload

    public var id: Int64 {
        questionID
    }

    public init(questionID: Int64, question: QuestionPayload) {
        self.questionID = questionID
        self.question = question
    }
}

/// AnswerPayload is the POST /api/subjects/{id}/answer body — the answer op's
/// payload minus the path `subject_id`. `selected` holds the chosen option labels;
/// `other` and `notes` are optional free text.
public struct AnswerPayload: Encodable, Equatable, Sendable {
    public let questionID: Int64
    public let selected: [String]
    public let other: String?
    public let notes: String?

    public init(questionID: Int64, selected: [String], other: String? = nil, notes: String? = nil) {
        self.questionID = questionID
        self.selected = selected
        self.other = other
        self.notes = notes
    }

    private enum CodingKeys: String, CodingKey {
        case questionID = "question_id"
        case selected
        case other
        case notes
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(questionID, forKey: .questionID)
        try container.encode(selected, forKey: .selected)
        try container.encodeIfPresent(other, forKey: .other)
        try container.encodeIfPresent(notes, forKey: .notes)
    }
}

/// APIError is a non-success REST response the caller can branch on.
public enum APIError: Error, Equatable {
    case nonHTTPResponse
    case status(code: Int, body: String)
}

/// APIClient talks to a cc-runtime daemon over its REST plane. It is a value type
/// with no mutable state, so it is freely shared across tasks. The prober hands in
/// the URLSession carrying the resolved leg's cert handling.
public struct APIClient: Sendable {
    public let baseURL: URL
    public let bearerToken: String?
    private let urlSession: URLSession

    /// Creates a client for the daemon at `baseURL` (e.g. `https://192.168.1.5:25444`).
    /// A `bearerToken`, when present, rides on the Authorization header of every
    /// request.
    public init(baseURL: URL, bearerToken: String? = nil, urlSession: URLSession = .shared) {
        self.baseURL = baseURL
        self.bearerToken = bearerToken
        self.urlSession = urlSession
    }

    /// sessions GETs /api/sessions, the daemon's roster of active subjects.
    public func sessions() async throws -> [Session] {
        let request = get("api/sessions")
        let (data, response) = try await urlSession.data(for: request)
        try APIClient.check(response, data: data)
        return try JSONDecoder().decode(SessionsReply.self, from: data).subjects
    }

    /// pending GETs /api/subjects/{subject}/pending, a subject's open questions with
    /// their string-encoded payloads.
    public func pending(subject: String) async throws -> [PendingQuestion] {
        let request = get("api/subjects/\(subject)/pending")
        let (data, response) = try await urlSession.data(for: request)
        try APIClient.check(response, data: data)
        return try JSONDecoder().decode(PendingReply.self, from: data).questions
    }

    /// openQuestions GETs a subject's pending questions and parses each string-encoded
    /// payload into its QuestionPayload, ready to render.
    public func openQuestions(subject: String) async throws -> [OpenQuestion] {
        try await pending(subject: subject).map {
            try OpenQuestion(questionID: $0.questionID, question: $0.question())
        }
    }

    /// answer POSTs an answer to /api/subjects/{subject}/answer and returns whether
    /// the subject idled (its last open question was answered). It throws APIError for
    /// a non-2xx reply, so a 404 unknown subject/question or a 400 malformed body
    /// surfaces to the caller.
    @discardableResult
    public func answer(subject: String, _ answer: AnswerPayload) async throws -> Bool {
        var request = get("api/subjects/\(subject)/answer")
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONEncoder().encode(answer)

        let (data, response) = try await urlSession.data(for: request)
        try APIClient.check(response, data: data)
        return try JSONDecoder().decode(AnswerReply.self, from: data).idled
    }

    private func get(_ path: String) -> URLRequest {
        var request = URLRequest(url: baseURL.appending(path: path))
        request.httpMethod = "GET"
        if let bearerToken {
            request.setValue("Bearer \(bearerToken)", forHTTPHeaderField: "Authorization")
        }
        return request
    }

    private static func check(_ response: URLResponse, data: Data) throws {
        guard let http = response as? HTTPURLResponse else {
            throw APIError.nonHTTPResponse
        }
        guard (200 ..< 300).contains(http.statusCode) else {
            throw APIError.status(code: http.statusCode, body: String(decoding: data, as: UTF8.self))
        }
    }
}

/// SessionsReply is the GET /api/sessions envelope.
private struct SessionsReply: Decodable {
    let subjects: [Session]
}

/// PendingReply is the GET /api/subjects/{id}/pending envelope.
private struct PendingReply: Decodable {
    let questions: [PendingQuestion]
}

/// AnswerReply is the POST answer response: whether the subject idled.
private struct AnswerReply: Decodable {
    let idled: Bool
}
