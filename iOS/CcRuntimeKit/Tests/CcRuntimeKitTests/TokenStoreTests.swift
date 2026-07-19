@testable import CcRuntimeKit
import Foundation
import Security
import Testing

@Suite("TokenStore")
struct TokenStoreTests {
    @Test("a written token round-trips and reads are idempotent")
    func roundTrip() throws {
        let machineID = uniqueMachineID()
        defer { try? TokenStore.deleteToken(machineID: machineID) }

        #expect(TokenStore.service == "com.yasyf.cc-runtime.token.v1")
        try TokenStore.setToken("secret-abc", machineID: machineID)
        #expect(try TokenStore.token(machineID: machineID) == "secret-abc")
        #expect(try TokenStore.token(machineID: machineID) == "secret-abc")
    }

    @Test("setToken replaces a prior value rather than duplicating the item")
    func setTokenReplaces() throws {
        let machineID = uniqueMachineID()
        defer { try? TokenStore.deleteToken(machineID: machineID) }

        try TokenStore.setToken("first", machineID: machineID)
        try TokenStore.setToken("second", machineID: machineID)
        #expect(try TokenStore.token(machineID: machineID) == "second")
    }

    @Test("a pre-v1 token requires explicit transfer and is never rewritten")
    func rejectsForeignIdentityWithoutMutation() throws {
        let machineID = uniqueMachineID()
        defer { deleteForeignItem(machineID: machineID) }
        addForeignItem(token: "foreign-token", machineID: machineID)

        #expect(throws: TokenStoreError.repairRequired(machineID: machineID)) {
            try TokenStore.token(machineID: machineID)
        }
        try TokenStore.setToken("replacement", machineID: machineID)
        #expect(try TokenStore.token(machineID: machineID) == "replacement")
        #expect(readRawToken(machineID: machineID, service: legacyService) == "foreign-token")
    }

    @Test("an absent token requires an actionable manual transfer or re-pair")
    func missingItemRequiresRepair() {
        let machineID = uniqueMachineID()
        #expect(throws: TokenStoreError.repairRequired(machineID: machineID)) {
            try TokenStore.token(machineID: machineID)
        }
    }

    @Test("deleteToken removes the item and is a no-op when nothing is stored")
    func deleteTokenIsIdempotent() throws {
        let machineID = uniqueMachineID()
        try TokenStore.setToken("bye", machineID: machineID)
        try TokenStore.deleteToken(machineID: machineID)
        #expect(throws: TokenStoreError.repairRequired(machineID: machineID)) {
            try TokenStore.token(machineID: machineID)
        }
        try TokenStore.deleteToken(machineID: machineID)
    }

    private func uniqueMachineID() -> String {
        "test-\(UUID().uuidString)"
    }

    private func addForeignItem(token: String, machineID: String) {
        let attributes: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: legacyService,
            kSecAttrAccount as String: machineID,
            kSecValueData as String: Data(token.utf8),
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlocked,
        ]
        SecItemDelete(attributes as CFDictionary)
        #expect(SecItemAdd(attributes as CFDictionary, nil) == errSecSuccess)
    }

    private func readRawToken(machineID: String, service: String) -> String? {
        var query = identityQuery(machineID: machineID, service: service)
        query[kSecReturnData as String] = true
        var result: CFTypeRef?
        #expect(SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess)
        guard let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private func deleteForeignItem(machineID: String) {
        try? TokenStore.deleteToken(machineID: machineID)
        SecItemDelete(identityQuery(machineID: machineID, service: legacyService) as CFDictionary)
    }

    private func identityQuery(machineID: String, service: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: machineID,
        ]
    }

    private var legacyService: String {
        "com.yasyf.cc-runtime"
    }
}
