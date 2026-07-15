// The SSE wire frame the client streams off GET /events. Each `data:` frame is a
// self-describing `{type, …payload}` object (interaction/wire.go): the type
// discriminator rides inside the payload because the streamed frame carries only
// the raw event payload, never the event's Type column. Event holds the frame's
// type plus its raw JSON so a consumer decodes the type-specific payload on demand
// with `decode(_:)`; the flattened payload fields decode straight into the matching
// struct (QuestionPayload, NotificationPayload, …), which ignores the extra `type`.

import Foundation

/// EventError is a failure lifting or decoding an SSE frame.
public enum EventError: Error, Equatable {
    case malformedWireFrame
}

/// Event is one decoded SSE frame: its `type` discriminant, the `seq` carried on
/// the SSE `id:` line, and the raw frame JSON. `decode(_:)` reads the type-specific
/// payload from the flattened frame body.
public struct Event: Equatable, Sendable {
    public let type: String
    public let seq: Int64?
    public let rawPayload: JSONValue

    public init(type: String, seq: Int64?, rawPayload: JSONValue) {
        self.type = type
        self.seq = seq
        self.rawPayload = rawPayload
    }

    /// wireFrame lifts a flat SSE `data:` frame into an Event. The frame's
    /// self-describing `type` sits alongside the payload fields; seq rides on the
    /// SSE `id:` line and is passed in.
    public static func wireFrame(_ data: Data, seq: Int64? = nil) throws -> Event {
        let raw = try JSONDecoder().decode(JSONValue.self, from: data)
        guard case let .object(fields) = raw, case let .string(type)? = fields["type"] else {
            throw EventError.malformedWireFrame
        }
        return Event(type: type, seq: seq, rawPayload: raw)
    }

    /// decode reads this frame's type-specific payload out of the flattened body.
    public func decode<T: Decodable>(_: T.Type) throws -> T {
        try rawPayload.decode(T.self)
    }
}

/// JSONValue is a decoded arbitrary JSON value, used to hold an event payload until
/// it is decoded into its typed struct. It re-encodes losslessly, keeping integers
/// integral so a seq or question id survives the round to a typed decode.
public enum JSONValue: Codable, Equatable, Sendable {
    case null
    case bool(Bool)
    case int(Int64)
    case double(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    public init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let value = try? container.decode(Bool.self) {
            self = .bool(value)
        } else if let value = try? container.decode(Int64.self) {
            self = .int(value)
        } else if let value = try? container.decode(Double.self) {
            self = .double(value)
        } else if let value = try? container.decode(String.self) {
            self = .string(value)
        } else if let value = try? container.decode([JSONValue].self) {
            self = .array(value)
        } else if let value = try? container.decode([String: JSONValue].self) {
            self = .object(value)
        } else {
            throw DecodingError.dataCorrupted(
                DecodingError.Context(codingPath: decoder.codingPath, debugDescription: "unrepresentable JSON value")
            )
        }
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .null: try container.encodeNil()
        case let .bool(value): try container.encode(value)
        case let .int(value): try container.encode(value)
        case let .double(value): try container.encode(value)
        case let .string(value): try container.encode(value)
        case let .array(value): try container.encode(value)
        case let .object(value): try container.encode(value)
        }
    }

    func decode<T: Decodable>(_: T.Type) throws -> T {
        let data = try JSONEncoder().encode(self)
        return try JSONDecoder().decode(T.self, from: data)
    }
}
