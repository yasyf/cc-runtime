// The narrow network seams the view models depend on, split from the concrete
// APIClient so a test injects a scripted fake without a socket. APIClient conforms
// to both, so production wires the real REST plane through unchanged.

import CcRuntimeKit
import Foundation

/// SessionsProviding is the one call the roster needs: the daemon's list of active
/// subjects.
protocol SessionsProviding: Sendable {
    func sessions() async throws -> [Session]
}

/// QuestionsProviding is what the subject surface needs: a subject's open questions
/// and the answer post.
protocol QuestionsProviding: Sendable {
    func openQuestions(subject: String) async throws -> [OpenQuestion]
    @discardableResult
    func answer(subject: String, _ answer: AnswerPayload) async throws -> Bool
}

extension APIClient: SessionsProviding, QuestionsProviding {}
