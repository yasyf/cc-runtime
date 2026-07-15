@testable import CcRuntimeKit
import Foundation
import Testing

@Suite("Device token registrar")
struct DeviceTokenRegistrarTests {
    @Test("hex encodes bytes lowercase, zero-padded")
    func hexEncoding() {
        #expect(DeviceTokenRegistrar.hex(Data([0x00, 0x0F, 0xA0, 0xFF])) == "000fa0ff")
    }

    @Test("register POSTs the hex token tagged platform ios to /api/push/device-tokens")
    func registerPostsToken() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }
        let recorder = RequestRecorder()
        StubURLProtocol.register(host: host) { request in
            recorder.record(request)
            return .json(200, "{}")
        }

        let registrar = try DeviceTokenRegistrar(
            baseURL: #require(URL(string: "https://\(host):25444")),
            bearerToken: "tok",
            urlSession: StubURLProtocol.session()
        )
        try await registrar.register(deviceToken: Data([0x01, 0xAB, 0xFF]))

        let request = try #require(recorder.requests.first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/api/push/device-tokens")
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer tok")

        let body = try #require(request.bodyData)
        let object = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(object["token"] as? String == "01abff")
        #expect(object["platform"] as? String == "ios")
    }

    @Test("a non-2xx reply surfaces as APIError.status")
    func nonSuccessThrows() async throws {
        let host = "stub-\(UUID().uuidString).test"
        defer { StubURLProtocol.unregister(host: host) }
        StubURLProtocol.register(host: host) { _ in .json(401, "unauthorized") }

        let registrar = try DeviceTokenRegistrar(
            baseURL: #require(URL(string: "https://\(host):25444")),
            bearerToken: nil,
            urlSession: StubURLProtocol.session()
        )
        await #expect(throws: APIError.status(code: 401, body: "unauthorized")) {
            try await registrar.register(hexToken: "01abff")
        }
    }
}
