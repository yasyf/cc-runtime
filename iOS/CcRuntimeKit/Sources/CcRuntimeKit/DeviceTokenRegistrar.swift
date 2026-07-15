// APNs device-token registration. When iOS hands the app its APNs device token (a
// Data blob), the app registers it with the connected daemon so the daemon can push
// to this device. The registrar POSTs the hex-encoded token to
// POST /api/push/device-tokens as `{"token":"<hex>","platform":"ios"}` on the
// resolved machine, bearer-authenticated like every other REST call.

import Foundation

/// DeviceTokenRegistrar registers this device's APNs token with a cc-runtime daemon.
public struct DeviceTokenRegistrar: Sendable {
    public let baseURL: URL
    public let bearerToken: String?
    private let urlSession: URLSession

    /// Creates a registrar for the daemon at `baseURL`. The prober hands in the
    /// URLSession carrying the resolved leg's cert handling.
    public init(baseURL: URL, bearerToken: String? = nil, urlSession: URLSession = .shared) {
        self.baseURL = baseURL
        self.bearerToken = bearerToken
        self.urlSession = urlSession
    }

    /// register POSTs the APNs device token, lowercase-hex encoded, tagged with
    /// platform "ios". It throws APIError for a non-2xx reply.
    public func register(deviceToken: Data) async throws {
        try await register(hexToken: DeviceTokenRegistrar.hex(deviceToken))
    }

    /// register POSTs an already-hex-encoded device token.
    public func register(hexToken: String) async throws {
        var request = URLRequest(url: baseURL.appending(path: "api/push/device-tokens"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let bearerToken {
            request.setValue("Bearer \(bearerToken)", forHTTPHeaderField: "Authorization")
        }
        request.httpBody = try JSONEncoder().encode(DeviceTokenBody(token: hexToken, platform: "ios"))

        let (data, response) = try await urlSession.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw APIError.nonHTTPResponse
        }
        guard (200 ..< 300).contains(http.statusCode) else {
            throw APIError.status(code: http.statusCode, body: String(decoding: data, as: UTF8.self))
        }
    }

    /// hex is the lowercase-hex encoding APNs device tokens use on the wire.
    public static func hex(_ data: Data) -> String {
        data.map { String(format: "%02x", $0) }.joined()
    }
}

/// DeviceTokenBody is the POST /api/push/device-tokens request body.
private struct DeviceTokenBody: Encodable {
    let token: String
    let platform: String
}
