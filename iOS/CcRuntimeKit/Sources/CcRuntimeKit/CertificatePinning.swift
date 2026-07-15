// Certificate pinning for the self-signed LAN leg. `cc-runtime pair` serves the LAN
// interfaces with a self-signed certificate and hands the client that cert's
// lowercase-hex SHA-256(DER) as the pairing payload's `fp`. The LAN leg carries no
// system-trust chain, so this delegate authenticates it by pinning: it accepts ONLY
// a server whose leaf-certificate fingerprint equals `fp`, and rejects everything
// else with no system-trust fallback for that host. The tailnet leg (a real
// certificate) uses a plain system-trust session instead — never this delegate.

import CryptoKit
import Foundation
import Security

/// CertificatePinner is the pure pinning decision, split out of the delegate so the
/// accept/reject rule is testable against a fixture SecTrust without a live TLS
/// handshake.
public enum CertificatePinner {
    /// fingerprint is the lowercase-hex SHA-256 of a certificate's DER encoding — the
    /// exact form `access.CertFingerprint` emits into the pairing payload.
    public static func fingerprint(ofDER der: Data) -> String {
        SHA256.hash(data: der).map { String(format: "%02x", $0) }.joined()
    }

    /// accepts reports whether `trust`'s leaf certificate has DER fingerprint
    /// `fingerprint`. A trust with no certificate chain, or a mismatched leaf, is
    /// rejected.
    public static func accepts(trust: SecTrust, fingerprint: String) -> Bool {
        guard
            let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
            let leaf = chain.first
        else {
            return false
        }
        let der = SecCertificateCopyData(leaf) as Data
        return CertificatePinner.fingerprint(ofDER: der) == fingerprint
    }
}

/// CertificatePinningDelegate authenticates a URLSession by pinning its server
/// certificate to a fixed fingerprint. It accepts a server-trust challenge only when
/// the leaf fingerprint matches, cancels it otherwise, and defers any non-server-trust
/// challenge to default handling.
public final class CertificatePinningDelegate: NSObject, URLSessionDelegate, @unchecked Sendable {
    private let fingerprint: String

    /// Creates a delegate pinning to `fingerprint` (lowercase-hex SHA-256 of the
    /// server leaf certificate's DER).
    public init(fingerprint: String) {
        self.fingerprint = fingerprint
    }

    public func urlSession(
        _: URLSession,
        didReceive challenge: URLAuthenticationChallenge,
        completionHandler: @escaping @Sendable (URLSession.AuthChallengeDisposition, URLCredential?) -> Void
    ) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust
        else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        if CertificatePinner.accepts(trust: trust, fingerprint: fingerprint) {
            completionHandler(.useCredential, URLCredential(trust: trust))
        } else {
            completionHandler(.cancelAuthenticationChallenge, nil)
        }
    }
}
