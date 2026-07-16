@testable import CcRuntimeApp
import CcRuntimeKit
import Foundation

/// InMemoryMachineRegistry is a MachineRegistry with no filesystem or Keychain, so
/// the pairing state machine and roster run in isolation. `addError`, when set, makes
/// the persist step fail so the failure branch is exercised.
final class InMemoryMachineRegistry: MachineRegistry, @unchecked Sendable {
    var machines: [Machine] = []
    var tokens: [String: String] = [:]
    var addError: Error?

    func load() throws -> [Machine] {
        machines
    }

    func add(_ machine: Machine, token: String) throws {
        if let addError {
            throw addError
        }
        machines.removeAll { $0.id == machine.id }
        machines.append(machine)
        tokens[machine.id] = token
    }

    func remove(_ machine: Machine) throws {
        machines.removeAll { $0.id == machine.id }
        tokens.removeValue(forKey: machine.id)
    }

    func token(for machineID: String) throws -> String? {
        tokens[machineID]
    }
}

/// FakeSessions is a scripted SessionsProviding: a fixed roster or a thrown error,
/// counting calls so a poll reload can be asserted.
final class FakeSessions: SessionsProviding, @unchecked Sendable {
    enum Outcome {
        case roster([Session])
        case failure(Error)
    }

    let outcome: Outcome
    private(set) var calls = 0

    init(_ outcome: Outcome) {
        self.outcome = outcome
    }

    func sessions() async throws -> [Session] {
        calls += 1
        switch outcome {
        case let .roster(list): return list
        case let .failure(error): throw error
        }
    }
}

/// FakeQuestions is a scripted QuestionsProviding: it serves a mutable open-question
/// list and records answers, optionally failing the answer post to exercise the
/// reconcile path. `holdNextOpen` snapshots the list, then suspends the fetch until
/// `releaseHeld`, so a test can interleave a stale refresh with a submit.
final class FakeQuestions: QuestionsProviding, @unchecked Sendable {
    var questions: [OpenQuestion]
    var idled: Bool
    var answerError: Error?
    var holdNextOpen = false
    private(set) var holding = false
    private var held: CheckedContinuation<Void, Never>?
    private(set) var answered: [AnswerPayload] = []

    init(questions: [OpenQuestion], idled: Bool = true) {
        self.questions = questions
        self.idled = idled
    }

    func openQuestions(subject _: String) async throws -> [OpenQuestion] {
        let snapshot = questions
        if holdNextOpen {
            holdNextOpen = false
            holding = true
            await withCheckedContinuation { held = $0 }
            holding = false
        }
        return snapshot
    }

    func releaseHeld() {
        held?.resume()
        held = nil
    }

    func answer(subject _: String, _ answer: AnswerPayload) async throws -> Bool {
        if let answerError {
            throw answerError
        }
        answered.append(answer)
        questions.removeAll { $0.questionID == answer.questionID }
        return idled
    }
}

func makeSession(subject: String, scope: String = "/work/proj", status: String, pending: Int = 0) -> Session {
    Session(subjectID: subject, scope: scope, status: status, pending: pending)
}

func makeQuestion(id: Int64, options: [Option] = [], multiSelect: Bool? = nil, prompt: String? = nil) -> OpenQuestion {
    OpenQuestion(
        questionID: id,
        question: QuestionPayload(header: nil, multiSelect: multiSelect, options: options, prompt: prompt)
    )
}
