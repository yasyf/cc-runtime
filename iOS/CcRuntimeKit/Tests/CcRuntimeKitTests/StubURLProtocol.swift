import Foundation

/// StubReply is a stubbed transport outcome: a status code with a JSON body, or a
/// transport failure that mimics an unreachable leg.
enum StubReply: Sendable {
    case status(Int, Data)
    case transportError

    static func json(_ code: Int, _ string: String) -> StubReply {
        .status(code, Data(string.utf8))
    }
}

/// RequestRecorder captures the requests a stub handled so a test can assert the
/// method, headers, and body after the async call returns. It is lock-guarded so the
/// URL loading thread and the test thread never race.
final class RequestRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var captured: [URLRequest] = []

    func record(_ request: URLRequest) {
        lock.withLock { captured.append(request) }
    }

    var requests: [URLRequest] {
        lock.withLock { captured }
    }
}

/// StubURLProtocol routes a session's requests to per-host handlers, so parallel
/// tests (each using a unique host) never collide. Install it by setting a session
/// configuration's `protocolClasses` to `[StubURLProtocol.self]`.
final class StubURLProtocol: URLProtocol {
    private static let lock = NSLock()
    private nonisolated(unsafe) static var routes: [String: @Sendable (URLRequest) -> StubReply] = [:]

    static func register(host: String, _ handler: @escaping @Sendable (URLRequest) -> StubReply) {
        lock.withLock { routes[host.lowercased()] = handler }
    }

    static func unregister(host: String) {
        lock.withLock { routes[host.lowercased()] = nil }
    }

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubURLProtocol.self]
        return URLSession(configuration: config)
    }

    private static func handler(for host: String) -> (@Sendable (URLRequest) -> StubReply)? {
        lock.withLock { routes[host.lowercased()] }
    }

    override class func canInit(with _: URLRequest) -> Bool {
        true
    }

    override class func canonicalRequest(for request: URLRequest) -> URLRequest {
        request
    }

    override func startLoading() {
        guard
            let url = request.url,
            let host = url.host(),
            let handler = StubURLProtocol.handler(for: host)
        else {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotFindHost))
            return
        }
        switch handler(request) {
        case let .status(code, data):
            guard let response = HTTPURLResponse(
                url: url,
                statusCode: code,
                httpVersion: "HTTP/1.1",
                headerFields: ["Content-Type": "application/json"]
            ) else {
                client?.urlProtocol(self, didFailWithError: URLError(.badServerResponse))
                return
            }
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        case .transportError:
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
        }
    }

    override func stopLoading() {}
}

extension URLRequest {
    /// bodyData reads the request body from either the buffered body or the body
    /// stream URLSession substitutes, so a stubbed POST can be asserted.
    var bodyData: Data? {
        if let httpBody {
            return httpBody
        }
        guard let stream = httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        let size = 4096
        let buffer = UnsafeMutablePointer<UInt8>.allocate(capacity: size)
        defer { buffer.deallocate() }
        while stream.hasBytesAvailable {
            let read = stream.read(buffer, maxLength: size)
            if read <= 0 {
                break
            }
            data.append(buffer, count: read)
        }
        return data
    }
}
