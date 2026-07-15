// A hand-rolled Server-Sent-Events client for cc-runtime's GET /events plane. The
// daemon (cc-interact's sse package) replays a subject's log from seq 0, marks the
// replay/live boundary with a named `caught-up` event carrying {seq}, then streams
// live frames with a `: keepalive` comment on an interval. This client parses that
// wire format over URLSession.bytes(for:), tracking the last event id so a dropped
// connection resumes past what it already saw, and reconnects with jittered
// exponential backoff. The SSEParser is factored out of the network path so the
// wire format can be exercised against hostile byte-chunk boundaries in isolation.

import Foundation

/// SSEClient streams a cc-runtime subject's event log over Server-Sent Events. One
/// `connect()` yields a message stream (the caught-up marker plus live frames) and a
/// connection-state stream; cancelling the task consuming the messages tears the
/// connection down. The client resumes with Last-Event-ID and reconnects on its own.
public actor SSEClient {
    /// Message is one decoded item from the stream: the replay/live boundary marker,
    /// or a live event frame.
    public enum Message: Equatable, Sendable {
        case caughtUp(seq: Int64)
        case frame(Event)
    }

    /// ConnectionState tracks the transport: connecting on the first dial,
    /// reconnecting while backing off after a drop, live once a stream is flowing.
    public enum ConnectionState: Equatable, Sendable {
        case connecting
        case live
        case reconnecting
    }

    /// Connection bundles the two streams a `connect()` produces. Consume `messages`
    /// to drive the connection; cancelling that consumer's task tears it down.
    public struct Connection: Sendable {
        public let messages: AsyncStream<Message>
        public let states: AsyncStream<ConnectionState>
    }

    private let eventsURL: URL
    private let bearerToken: String?
    private let urlSession: URLSession
    private let requestTimeout: TimeInterval

    private var lastEventID: String?
    private var backoff = 0
    private var retryHintMillis: Int?

    /// Creates a client for the subject `session` at `baseURL` (e.g.
    /// `https://192.168.1.5:25444`). A `bearerToken` rides on the Authorization
    /// header. A caller may supply its own `urlSession` — the endpoint prober hands
    /// in the one carrying the resolved leg's cert handling; the default one is
    /// tuned for SSE, with a per-request idle timeout comfortably above the keepalive
    /// and no overall resource timeout, so a healthy stream lives indefinitely.
    public init(
        baseURL: URL,
        session: String,
        bearerToken: String? = nil,
        urlSession: URLSession? = nil,
        requestTimeout: TimeInterval = 60
    ) {
        eventsURL = baseURL
            .appending(path: "events")
            .appending(queryItems: [URLQueryItem(name: "session", value: session)])
        self.bearerToken = bearerToken
        self.requestTimeout = requestTimeout
        self.urlSession = urlSession ?? SSEClient.makeSession(requestTimeout: requestTimeout)
    }

    /// connect starts the connection loop and returns its message and state streams.
    /// The loop runs until the messages stream is cancelled or dropped; on any
    /// transport drop it reconnects with jittered exponential backoff, resuming from
    /// the last event id it dispatched.
    public func connect() -> Connection {
        let (states, stateContinuation) = AsyncStream<ConnectionState>.makeStream(bufferingPolicy: .bufferingNewest(8))
        let (messages, messageContinuation) = AsyncStream<Message>.makeStream(bufferingPolicy: .unbounded)
        let task = Task { await self.runLoop(messages: messageContinuation, states: stateContinuation) }
        messageContinuation.onTermination = { _ in task.cancel() }
        return Connection(messages: messages, states: states)
    }

    private func runLoop(
        messages: AsyncStream<Message>.Continuation,
        states: AsyncStream<ConnectionState>.Continuation
    ) async {
        defer {
            messages.finish()
            states.finish()
        }
        states.yield(.connecting)
        while !Task.isCancelled {
            do {
                try await streamOnce(messages: messages, states: states)
            } catch {
                if Task.isCancelled {
                    return
                }
            }
            if Task.isCancelled {
                return
            }
            states.yield(.reconnecting)
            let delay = backoffNanos()
            backoff += 1
            do {
                try await Task.sleep(nanoseconds: delay)
            } catch {
                return
            }
        }
    }

    private func streamOnce(
        messages: AsyncStream<Message>.Continuation,
        states: AsyncStream<ConnectionState>.Continuation
    ) async throws {
        var request = URLRequest(
            url: eventsURL,
            cachePolicy: .reloadIgnoringLocalCacheData,
            timeoutInterval: requestTimeout
        )
        request.httpMethod = "GET"
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        request.setValue("no-cache", forHTTPHeaderField: "Cache-Control")
        if let bearerToken {
            request.setValue("Bearer \(bearerToken)", forHTTPHeaderField: "Authorization")
        }
        if let lastEventID {
            request.setValue(lastEventID, forHTTPHeaderField: "Last-Event-ID")
        }

        let (bytes, response) = try await urlSession.bytes(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw SSEClientError.nonHTTPResponse
        }
        guard http.statusCode == 200 else {
            throw SSEClientError.unexpectedStatus(http.statusCode)
        }
        states.yield(.live)

        var parser = SSEParser()
        defer {
            if let hint = parser.reconnectionTime {
                retryHintMillis = hint
            }
        }
        for try await byte in bytes {
            parser.feed(CollectionOfOne(byte)) { event in
                guard let message = SSEClient.message(from: event) else { return }
                if case .caughtUp = message {
                    backoff = 0
                }
                messages.yield(message)
            }
            lastEventID = parser.lastEventID
        }
    }

    private func backoffNanos() -> UInt64 {
        let baseMillis = Double(retryHintMillis ?? 1000)
        let capMillis = 30000.0
        let ceiling = min(capMillis, baseMillis * pow(2.0, Double(backoff)))
        let jittered = Double.random(in: 0 ... max(ceiling, 1))
        return UInt64(jittered * 1_000_000)
    }

    /// message maps one parsed SSE event onto the client's Message enum: a
    /// `caught-up` named event decodes to its boundary seq, a default frame lifts to
    /// an Event stamped with the frame's id, and anything else is dropped.
    static func message(from event: SSEEvent) -> Message? {
        switch event.event {
        case "caught-up":
            guard let marker = try? JSONDecoder().decode(CaughtUp.self, from: Data(event.data.utf8)) else {
                return nil
            }
            return .caughtUp(seq: marker.seq)
        case "":
            let seq = event.lastEventID.flatMap { Int64($0) }
            guard let frame = try? Event.wireFrame(Data(event.data.utf8), seq: seq) else {
                return nil
            }
            return .frame(frame)
        default:
            return nil
        }
    }

    private struct CaughtUp: Decodable {
        let seq: Int64
    }

    private static func makeSession(requestTimeout: TimeInterval) -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = requestTimeout
        config.timeoutIntervalForResource = .infinity
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.waitsForConnectivity = true
        return URLSession(configuration: config)
    }
}

/// SSEClientError is a transport failure the connection loop recovers from by
/// reconnecting; it never reaches a stream consumer.
enum SSEClientError: Error, Equatable {
    case nonHTTPResponse
    case unexpectedStatus(Int)
}

/// SSEEvent is one dispatched Server-Sent event: its name (`""` for the default
/// `message` type), its joined data payload with the single trailing newline
/// stripped, and the reconnect id in effect when it dispatched.
struct SSEEvent: Equatable, Sendable {
    var event: String
    var data: String
    var lastEventID: String?
}

/// SSEParser turns a byte stream into dispatched SSEEvents per the WHATWG
/// event-stream grammar. It is fed arbitrary byte chunks — splits mid-line,
/// mid-field, and mid-UTF-8-rune all survive, because bytes accumulate until a
/// complete line (a line terminator can only be ASCII) is decoded. Comment lines
/// (`: keepalive`) and blank-line dispatches with no data emit nothing. The
/// reconnect id (`lastEventID`) advances only when a frame fully dispatches, so a
/// partially received frame never poisons the resume cursor.
struct SSEParser {
    private var buffer: [UInt8] = []
    private var scan = 0
    private var lineStart = 0

    private var dataBuffer = ""
    private var eventType = ""
    private var idBuffer: String?

    /// lastEventID is the reconnect cursor: the id buffer copied at each dispatch,
    /// the value to send as Last-Event-ID on resume.
    private(set) var lastEventID: String?

    /// reconnectionTime is the most recent `retry:` value in milliseconds, if any.
    private(set) var reconnectionTime: Int?

    private static let lineFeed: UInt8 = 0x0A
    private static let carriageReturn: UInt8 = 0x0D

    /// feed consumes a chunk of bytes and invokes `emit` for each event the chunk
    /// completes. Bytes that do not yet form a complete line are retained for the
    /// next call.
    mutating func feed(_ bytes: some Sequence<UInt8>, emit: (SSEEvent) -> Void) {
        buffer.append(contentsOf: bytes)
        while scan < buffer.count {
            let byte = buffer[scan]
            if byte == Self.lineFeed {
                dispatchLine(through: scan, emit: emit)
                scan += 1
                lineStart = scan
            } else if byte == Self.carriageReturn {
                // A lone CR at the buffer's edge might be the first half of a CRLF
                // whose LF lands in the next chunk; wait for it rather than split.
                guard scan + 1 < buffer.count else { break }
                dispatchLine(through: scan, emit: emit)
                scan += buffer[scan + 1] == Self.lineFeed ? 2 : 1
                lineStart = scan
            } else {
                scan += 1
            }
        }
        if lineStart > 0 {
            buffer.removeFirst(lineStart)
            scan -= lineStart
            lineStart = 0
        }
    }

    /// feed consumes a chunk and returns the events it completes.
    mutating func feed(_ bytes: some Sequence<UInt8>) -> [SSEEvent] {
        var events: [SSEEvent] = []
        feed(bytes) { events.append($0) }
        return events
    }

    private mutating func dispatchLine(through terminator: Int, emit: (SSEEvent) -> Void) {
        let line = String(decoding: buffer[lineStart ..< terminator], as: UTF8.self)
        handleLine(line, emit: emit)
    }

    private mutating func handleLine(_ line: String, emit: (SSEEvent) -> Void) {
        if line.isEmpty {
            if let event = dispatch() {
                emit(event)
            }
            return
        }
        if line.hasPrefix(":") {
            return
        }

        let field: String
        var value: String
        if let colon = line.firstIndex(of: ":") {
            field = String(line[..<colon])
            value = String(line[line.index(after: colon)...])
            if value.hasPrefix(" ") {
                value.removeFirst()
            }
        } else {
            field = line
            value = ""
        }

        switch field {
        case "event":
            eventType = value
        case "data":
            dataBuffer += value
            dataBuffer += "\n"
        case "id":
            if !value.contains("\u{0}") {
                idBuffer = value
            }
        case "retry":
            if !value.isEmpty, value.allSatisfy({ $0.isASCII && $0.isNumber }), let millis = Int(value) {
                reconnectionTime = millis
            }
        default:
            break
        }
    }

    private mutating func dispatch() -> SSEEvent? {
        lastEventID = idBuffer
        guard !dataBuffer.isEmpty else {
            eventType = ""
            return nil
        }
        if dataBuffer.hasSuffix("\n") {
            dataBuffer.removeLast()
        }
        let event = SSEEvent(event: eventType, data: dataBuffer, lastEventID: lastEventID)
        dataBuffer = ""
        eventType = ""
        return event
    }
}
