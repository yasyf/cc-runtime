import CcRuntimeKit
import SwiftUI

/// SubjectModel loads one subject's open questions over REST and submits answers.
/// A submit optimistically drops the answered question; the reply's idled flag tells
/// the view the subject released its gate once the last question is answered. A
/// failed submit reconciles by reloading.
@MainActor
@Observable
final class SubjectModel {
    /// Phase is the pending-list load state.
    enum Phase: Equatable {
        case loading
        case loaded
        case failed(String)
    }

    let subject: String

    private(set) var phase: Phase = .loading
    private(set) var questions: [OpenQuestion] = []
    private(set) var idled = false
    private(set) var submitError: String?

    private let client: any QuestionsProviding
    private var inFlight: Set<Int64> = []

    init(subject: String, client: any QuestionsProviding) {
        self.subject = subject
        self.client = client
    }

    /// refresh reloads the open questions, holding the prior list visible across the
    /// reload. A non-empty result clears the idled banner.
    func refresh() async {
        do {
            let fetched = try await client.openQuestions(subject: subject)
            questions = fetched
            if !fetched.isEmpty {
                idled = false
            }
            phase = .loaded
        } catch {
            phase = .failed(SubjectModel.message(for: error))
        }
    }

    /// isSubmitting reports whether a given question's answer post is in flight, so
    /// the card can show its sending state and block a double submit.
    func isSubmitting(_ questionID: Int64) -> Bool {
        inFlight.contains(questionID)
    }

    /// submit posts an answer, optimistically removing the question. On success it
    /// records whether the subject idled; on failure it reloads to restore the true
    /// pending set and surfaces the error.
    func submit(_ open: OpenQuestion, draft: AnswerDraft) async {
        guard !inFlight.contains(open.questionID) else {
            return
        }
        inFlight.insert(open.questionID)
        defer { inFlight.remove(open.questionID) }
        submitError = nil
        let remaining = questions.filter { $0.questionID != open.questionID }
        questions = remaining
        do {
            let didIdle = try await client.answer(subject: subject, draft.payload(questionID: open.questionID))
            if remaining.isEmpty {
                idled = didIdle
            }
        } catch {
            submitError = SubjectModel.message(for: error)
            await refresh()
        }
    }

    private static func message(for error: Error) -> String {
        if case let APIError.status(code, body) = error {
            let detail = body.trimmingCharacters(in: .whitespacesAndNewlines)
            return detail.isEmpty ? "The machine returned an error (\(code))." : "Error \(code): \(detail)"
        }
        return "Couldn't reach this machine."
    }
}

/// SubjectView is the answer surface: the subject's open questions rendered with full
/// context, each answerable inline. It polls the pending list for liveness (the
/// machine's stream hub owns the SSE), and shows the idled banner once the subject
/// releases its gate. It is only reached under a connected machine, so the REST
/// client resolves on first appearance.
struct SubjectView: View {
    let connection: MachineConnection
    let feed: FeedStore
    let subject: String

    @State private var model: SubjectModel?

    init(connection: MachineConnection, feed: FeedStore, subject: String) {
        self.connection = connection
        self.feed = feed
        self.subject = subject
    }

    var body: some View {
        Group {
            if let model {
                content(model)
                    .refreshable { await model.refresh() }
            } else {
                ProgressView("Loading questions…")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .navigationTitle(scopeLabel(subject))
        .navigationBarTitleDisplayMode(.inline)
        .task { await poll() }
    }

    @ViewBuilder
    private func content(_ model: SubjectModel) -> some View {
        switch model.phase {
        case .loading:
            ProgressView("Loading questions…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case let .failed(message):
            ContentUnavailableView {
                Label("Can't Load Questions", systemImage: "wifi.exclamationmark")
            } description: {
                Text(message)
            } actions: {
                Button("Try Again") { Task { await model.refresh() } }
            }
        case .loaded:
            loaded(model)
        }
    }

    @ViewBuilder
    private func loaded(_ model: SubjectModel) -> some View {
        if model.questions.isEmpty {
            ContentUnavailableView {
                Label(
                    model.idled ? "Answered" : "Nothing to Answer",
                    systemImage: model.idled ? "checkmark.circle" : "tray"
                )
            } description: {
                Text(model.idled
                    ? "You answered every open question; the agent has moved on."
                    : "This session has no open questions right now.")
            }
        } else {
            ScrollView {
                LazyVStack(spacing: 16) {
                    if let submitError = model.submitError {
                        Label(submitError, systemImage: "exclamationmark.triangle")
                            .font(.footnote)
                            .foregroundStyle(.red)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    ForEach(model.questions) { open in
                        QuestionCard(
                            open: open,
                            submitting: model.isSubmitting(open.questionID)
                        ) { draft in
                            await model.submit(open, draft: draft)
                        }
                    }
                }
                .padding()
            }
        }
    }

    private func poll() async {
        if model == nil {
            guard let client = connection.apiClient else {
                return
            }
            model = SubjectModel(subject: subject, client: client)
        }
        guard let model else {
            return
        }
        while !Task.isCancelled {
            await model.refresh()
            try? await Task.sleep(for: .seconds(3))
        }
    }
}

/// QuestionCard renders one open question's full context — header, prompt, reasoning,
/// diff — and its answer controls: options as toggle buttons honoring multiSelect,
/// plus free-text and notes. It owns the in-progress AnswerDraft and hands it to the
/// submit closure.
struct QuestionCard: View {
    let open: OpenQuestion
    let submitting: Bool
    let onSubmit: (AnswerDraft) async -> Void

    @State private var draft = AnswerDraft()
    @FocusState private var focused: Bool

    private var question: QuestionPayload {
        open.question
    }

    private var hasOptions: Bool {
        !question.options.isEmpty
    }

    private var multiSelect: Bool {
        question.multiSelect ?? false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            context
            if hasOptions {
                options
            }
            fields
            submit
        }
        .padding()
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 14))
    }

    @ViewBuilder
    private var context: some View {
        if let header = question.header, !header.isEmpty {
            Text(header.uppercased())
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.tint)
        }
        if let prompt = question.prompt, !prompt.isEmpty {
            Text(prompt)
                .font(.headline)
        }
        if let reasoning = question.reasoning, !reasoning.isEmpty {
            VStack(alignment: .leading, spacing: 2) {
                Text("Reasoning")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
                Text(reasoning)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }
        }
        if let diff = question.diff, !diff.isEmpty {
            DiffView(diff: diff)
        }
    }

    private var options: some View {
        VStack(spacing: 8) {
            ForEach(question.options, id: \.label) { option in
                let isSelected = draft.selected.contains(option.label)
                Button {
                    draft.toggle(option.label, multiSelect: multiSelect)
                } label: {
                    HStack(alignment: .top, spacing: 10) {
                        Image(systemName: optionSymbol(isSelected: isSelected))
                            .foregroundStyle(isSelected ? AnyShapeStyle(.tint) : AnyShapeStyle(.secondary))
                        VStack(alignment: .leading, spacing: 2) {
                            Text(option.label)
                                .font(.body.weight(.medium))
                            if let description = option.description, !description.isEmpty {
                                Text(description)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            if let preview = option.preview, !preview.isEmpty {
                                Text(preview)
                                    .font(.caption.monospaced())
                                    .foregroundStyle(.secondary)
                            }
                        }
                        Spacer(minLength: 0)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(10)
                    .background(
                        RoundedRectangle(cornerRadius: 10)
                            .strokeBorder(isSelected ? AnyShapeStyle(.tint) : AnyShapeStyle(.quaternary))
                    )
                }
                .buttonStyle(.plain)
            }
            if multiSelect {
                Text("Select all that apply")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    private var fields: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(hasOptions ? "Other answer" : "Your answer")
                .font(.caption.weight(.medium))
                .foregroundStyle(.secondary)
            TextField(
                hasOptions ? "Type an answer instead of picking…" : "Type your answer…",
                text: $draft.other,
                axis: .vertical
            )
            .lineLimit(hasOptions ? 1 ... 3 : 3 ... 6)
            .textFieldStyle(.roundedBorder)
            .focused($focused)

            Text("Notes")
                .font(.caption.weight(.medium))
                .foregroundStyle(.secondary)
            TextField("Optional notes…", text: $draft.notes, axis: .vertical)
                .lineLimit(1 ... 3)
                .textFieldStyle(.roundedBorder)
                .focused($focused)
        }
    }

    private var submit: some View {
        Button {
            focused = false
            Task { await onSubmit(draft) }
        } label: {
            HStack {
                Spacer()
                if submitting {
                    ProgressView()
                } else {
                    Text("Submit answer")
                }
                Spacer()
            }
        }
        .buttonStyle(.borderedProminent)
        .disabled(!draft.isFilled || submitting)
    }

    private func optionSymbol(isSelected: Bool) -> String {
        if multiSelect {
            return isSelected ? "checkmark.square.fill" : "square"
        }
        return isSelected ? "largecircle.fill.circle" : "circle"
    }
}

/// DiffView renders a unified diff with per-line coloring, matching the web card.
struct DiffView: View {
    let diff: String

    private var lines: [String] {
        diff.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
    }

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                    Text(line.isEmpty ? " " : line)
                        .font(.caption.monospaced())
                        .foregroundStyle(color(for: line))
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .padding(8)
        }
        .background(.background.tertiary, in: RoundedRectangle(cornerRadius: 8))
    }

    private func color(for line: String) -> Color {
        switch diffLineKind(line) {
        case .addition: .green
        case .deletion: .red
        case .hunk: .purple
        case .context: .secondary
        }
    }
}
