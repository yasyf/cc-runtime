@testable import CcRuntimeKit
import Foundation
import Security
import Testing

@Suite("Certificate pinning")
struct PinningTests {
    // A self-signed fixture certificate (DER, base64) and its known lowercase-hex
    // SHA-256(DER) — the exact fingerprint form `access.CertFingerprint` emits.
    // swiftlint:disable:next line_length
    private static let fixtureDER = "MIIDFTCCAf2gAwIBAgIUBMd+5WBafRa/AthbpP8Jug6SHxMwDQYJKoZIhvcNAQELBQAwGjEYMBYGA1UEAwwPY2MtcnVudGltZS10ZXN0MB4XDTI2MDcxNTIxNDYxN1oXDTM2MDcxMjIxNDYxN1owGjEYMBYGA1UEAwwPY2MtcnVudGltZS10ZXN0MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAvpLli05Xz3X7YFyc2azOqm6oh6o+hgmtD9uslzgUTwwbX8V03G4hrp+TyY6Ws2FN5ifiuntx+UxUbmvSM+/mjECKeau9IJJC4JK/wkZgc3cILAycNLhEUwlGMlTYgnesY017c7xwlLxpr40iOl8xsN6llV84kFMLhAaAbutjuO8Y8T2xXa4MAZzsGrPJvGGS8OTeeQg1cdmHyAUkU2WBrvy/5l2Ltx0ijSCyIAW1aeVDP3X46S1EpUWZXziVE9NnB5VT8lgB3jWq2D0jyS/Vq8U4QgN6A/AKMctiQiJyuGOvqDObguW3F3p+UIinE6ETN2hj/uhs87y+IdEQAH98RwIDAQABo1MwUTAdBgNVHQ4EFgQUNuRhYJ+x5Oi5x24hLRFJRgGRPfEwHwYDVR0jBBgwFoAUNuRhYJ+x5Oi5x24hLRFJRgGRPfEwDwYDVR0TAQH/BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAQEAAQUXdGy5EUl1HCl+f3oV28rOIRkQ2+wwdE0GYXo6Ykti3YmAGCnMZS9EWDLubFHcU2M3BC10LJeeFLEX1FePbPOtLzmAmNjfhRHXa/1XyyNcotYfC8Cg4LVvQSkOOQ7e7gSq26qPaTYwIBEZqa8oYJ9PIEWXUvXNAbqGp6VKNxmcmce+pfIM4QxJa+o9Civ2WgYD0H0TkiindZkYe5qIvFZqXNdCtoCe2BNEAQcBJ8BmnfaOz1rclcSNAO8z3rYcC35UKL7JfeS+2PcdAav4odZ7kp4Zm3suB592EMRbCRLaesiznV5h7Z2KZg3dKPtukGK7ozjVyvocC0C1Yx5PGw=="

    private static let fixtureFingerprint = "de7be0e6bc5e68314ebb3d08d1380d6bb1b02a01ecf23f8d4560ecb99c2cf247"

    @Test("the fingerprint of the fixture DER matches the daemon's lowercase-hex form")
    func fingerprintMatchesDaemonForm() throws {
        let der = try #require(Data(base64Encoded: PinningTests.fixtureDER))
        #expect(CertificatePinner.fingerprint(ofDER: der) == PinningTests.fixtureFingerprint)
    }

    @Test("a server trust whose leaf matches the pin is accepted")
    func acceptsMatchingLeaf() throws {
        let trust = try Self.fixtureTrust()
        #expect(CertificatePinner.accepts(trust: trust, fingerprint: PinningTests.fixtureFingerprint))
    }

    @Test("a mismatched fingerprint is rejected — no system-trust fallback")
    func rejectsMismatchedFingerprint() throws {
        let trust = try Self.fixtureTrust()
        #expect(!CertificatePinner.accepts(trust: trust, fingerprint: "00" + PinningTests.fixtureFingerprint.dropFirst(2)))
        #expect(!CertificatePinner.accepts(trust: trust, fingerprint: "deadbeef"))
    }

    @Test("pinning is case-sensitive: an uppercase fingerprint does not match")
    func rejectsUppercaseFingerprint() throws {
        let trust = try Self.fixtureTrust()
        #expect(!CertificatePinner.accepts(trust: trust, fingerprint: PinningTests.fixtureFingerprint.uppercased()))
    }

    private static func fixtureTrust() throws -> SecTrust {
        let der = try #require(Data(base64Encoded: fixtureDER))
        let certificate = try #require(SecCertificateCreateWithData(nil, der as CFData))
        var trust: SecTrust?
        let status = SecTrustCreateWithCertificates(certificate, SecPolicyCreateBasicX509(), &trust)
        #expect(status == errSecSuccess)
        return try #require(trust)
    }
}
