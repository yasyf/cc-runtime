@testable import CcRuntimeKit
import Foundation
import Security
import Testing

@Suite("TokenStore")
struct TokenStoreTests {
    @Test(
        "needsUpgrade fires only on a present, non-hardened accessibility class",
        arguments: [
            (kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly as String, false),
            (kSecAttrAccessibleWhenUnlocked as String, true),
            (kSecAttrAccessibleAfterFirstUnlock as String, true),
            (kSecAttrAccessibleWhenUnlockedThisDeviceOnly as String, true),
        ]
    )
    func needsUpgradeDecision(accessibility: String, expected: Bool) {
        #expect(TokenStore.needsUpgrade(accessibility) == expected)
    }

    @Test("needsUpgrade leaves an unreadable accessibility attribute untouched")
    func needsUpgradeMissing() {
        #expect(TokenStore.needsUpgrade(nil) == false)
        #expect(TokenStore.needsUpgrade(42) == false)
    }

    @Test("a written token round-trips and reads are idempotent")
    func roundTrip() throws {
        let machineID = uniqueMachineID()
        defer { try? TokenStore.deleteToken(machineID: machineID) }

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

    @Test("reading a legacy item (written with no accessibility class) preserves the token")
    func readsLegacyItemWithoutLoss() throws {
        let machineID = uniqueMachineID()
        defer { try? TokenStore.deleteToken(machineID: machineID) }
        addLegacyItem(token: "legacy-token", machineID: machineID)

        #expect(try TokenStore.token(machineID: machineID) == "legacy-token")
        #expect(try TokenStore.token(machineID: machineID) == "legacy-token")
    }

    @Test("token is nil for a machine that was never paired")
    func missingItemReturnsNil() throws {
        #expect(try TokenStore.token(machineID: uniqueMachineID()) == nil)
    }

    @Test("deleteToken removes the item and is a no-op when nothing is stored")
    func deleteTokenIsIdempotent() throws {
        let machineID = uniqueMachineID()
        try TokenStore.setToken("bye", machineID: machineID)
        try TokenStore.deleteToken(machineID: machineID)
        #expect(try TokenStore.token(machineID: machineID) == nil)
        try TokenStore.deleteToken(machineID: machineID)
    }

    private func uniqueMachineID() -> String {
        "test-\(UUID().uuidString)"
    }

    private func addLegacyItem(token: String, machineID: String) {
        let attributes: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: TokenStore.service,
            kSecAttrAccount as String: machineID,
            kSecValueData as String: Data(token.utf8),
        ]
        SecItemDelete(attributes as CFDictionary)
        #expect(SecItemAdd(attributes as CFDictionary, nil) == errSecSuccess)
    }
}
