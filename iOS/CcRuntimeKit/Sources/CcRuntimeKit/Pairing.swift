// Pairing: how the app remembers a daemon it scanned. A Machine (the identity, its
// ordered candidate URLs, and the LAN-leg cert fingerprint) persists as JSON under
// Application Support; its bearer token lives in the Keychain as a generic-password
// item keyed by machine id, so the secret never touches the JSON file. PairPayload
// decodes the QR the daemon renders. The Keychain path uses only portable
// Security-framework calls, so it compiles — and `swift test` runs — on macOS as
// well as iOS, with no app entitlement required.

import Foundation
import Security

/// PairPayload is the QR-encoded handshake `cc-runtime pair` renders:
/// `{"v":1,"name":"…","urls":["https://…",…],"token":"…","fp":"…"}`. `urls` is an
/// ordered candidate list (LAN HTTPS first, tailnet HTTPS when available); `fp` is
/// the lowercase-hex SHA-256 of the self-signed LAN certificate's DER a client
/// pins to authenticate the LAN leg. Decoding rejects any version but 1, any
/// non-HTTPS URL, and an IP-literal URL with no fingerprint to pin — the producer
/// always emits `fp` alongside its IP-literal LAN legs, so a payload missing it is
/// tampered or corrupt. A hostname-only payload needs no fingerprint: that leg
/// authenticates via system trust.
public struct PairPayload: Decodable, Equatable, Sendable {
    public let version: Int
    public let name: String
    public let urls: [URL]
    public let token: String
    public let fingerprint: String?

    public init(version: Int, name: String, urls: [URL], token: String, fingerprint: String? = nil) {
        self.version = version
        self.name = name
        self.urls = urls
        self.token = token
        self.fingerprint = fingerprint
    }

    private enum CodingKeys: String, CodingKey {
        case version = "v"
        case name
        case urls
        case token
        case fingerprint = "fp"
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let decoded = try container.decode(Int.self, forKey: .version)
        guard decoded == 1 else { throw PairError.unsupportedVersion(decoded) }
        version = decoded
        name = try container.decode(String.self, forKey: .name)
        urls = try container.decode([URL].self, forKey: .urls)
        token = try container.decode(String.self, forKey: .token)
        fingerprint = try container.decodeIfPresent(String.self, forKey: .fingerprint)
        if let insecure = urls.first(where: { $0.scheme?.lowercased() != "https" }) {
            throw PairError.insecureURL(insecure)
        }
        if urls.contains(where: EndpointProber.pins), (fingerprint ?? "").isEmpty {
            throw PairError.missingFingerprint
        }
    }
}

/// PairError is a rejected pairing payload.
public enum PairError: Error, Equatable {
    case unsupportedVersion(Int)
    case insecureURL(URL)
    case missingFingerprint
}

/// Machine is a paired daemon the app remembers: a stable id (the Keychain account
/// for its token), a human name, the ordered candidate URLs the prober races, and
/// the LAN-leg cert fingerprint it pins. The token is stored separately in the
/// Keychain, never in this record.
public struct Machine: Codable, Equatable, Sendable, Identifiable {
    public let id: String
    public var name: String
    public var urls: [URL]
    public var fingerprint: String?

    public init(id: String = UUID().uuidString, name: String, urls: [URL], fingerprint: String? = nil) {
        self.id = id
        self.name = name
        self.urls = urls
        self.fingerprint = fingerprint
    }

    /// Creates a Machine from a scanned pairing payload, minting a fresh id.
    public init(payload: PairPayload) {
        self.init(name: payload.name, urls: payload.urls, fingerprint: payload.fingerprint)
    }
}

private let machineStateIdentity = "dev.yasyf.cc-runtime.machines"
private let machineStateVersion = 1
/// SHA-256 of identity, NUL, and the exact v1 schema declaration.
private let machineStateFingerprint = "0b69553ff8c2f9f2168dfd37020e7d312eb5bae38321449e8236b62883d46ab0"

private struct AnyCodingKey: CodingKey {
    let stringValue: String
    let intValue: Int? = nil

    init?(stringValue: String) {
        self.stringValue = stringValue
    }

    init?(intValue _: Int) {
        nil
    }
}

private func requireExactKeys(_ decoder: Decoder, _ expected: Set<String>) throws {
    let container = try decoder.container(keyedBy: AnyCodingKey.self)
    let actual = Set(container.allKeys.map(\.stringValue))
    guard actual == expected else {
        let missing = expected.subtracting(actual).sorted()
        let unknown = actual.subtracting(expected).sorted()
        throw DecodingError.dataCorrupted(.init(
            codingPath: decoder.codingPath,
            debugDescription: "exact object keys mismatch: missing=\(missing), unknown=\(unknown)"
        ))
    }
}

private struct MachineStateSchema: Codable {
    let identity: String
    let version: Int
    let fingerprint: String

    private enum CodingKeys: String, CodingKey, CaseIterable {
        case identity, version, fingerprint
    }

    init() {
        identity = machineStateIdentity
        version = machineStateVersion
        fingerprint = machineStateFingerprint
    }

    init(from decoder: Decoder) throws {
        try requireExactKeys(decoder, Set(CodingKeys.allCases.map(\.rawValue)))
        let container = try decoder.container(keyedBy: CodingKeys.self)
        identity = try container.decode(String.self, forKey: .identity)
        version = try container.decode(Int.self, forKey: .version)
        fingerprint = try container.decode(String.self, forKey: .fingerprint)
        guard identity == machineStateIdentity,
              version == machineStateVersion,
              fingerprint == machineStateFingerprint
        else {
            throw DecodingError.dataCorrupted(.init(
                codingPath: decoder.codingPath,
                debugDescription: "machines state schema mismatch"
            ))
        }
    }
}

private struct PersistedMachine: Codable {
    let id: String
    let name: String
    let urls: [URL]
    let fingerprint: String?

    private enum CodingKeys: String, CodingKey, CaseIterable {
        case id, name, urls, fingerprint
    }

    init(_ machine: Machine) {
        id = machine.id
        name = machine.name
        urls = machine.urls
        fingerprint = machine.fingerprint
    }

    init(from decoder: Decoder) throws {
        try requireExactKeys(decoder, Set(CodingKeys.allCases.map(\.rawValue)))
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(String.self, forKey: .id)
        name = try container.decode(String.self, forKey: .name)
        urls = try container.decode([URL].self, forKey: .urls)
        fingerprint = try container.decode(String?.self, forKey: .fingerprint)
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(id, forKey: .id)
        try container.encode(name, forKey: .name)
        try container.encode(urls, forKey: .urls)
        if let fingerprint {
            try container.encode(fingerprint, forKey: .fingerprint)
        } else {
            try container.encodeNil(forKey: .fingerprint)
        }
    }

    var machine: Machine {
        Machine(id: id, name: name, urls: urls, fingerprint: fingerprint)
    }
}

private struct MachineStateEnvelope: Codable {
    let schema: MachineStateSchema
    let machines: [PersistedMachine]

    private enum CodingKeys: String, CodingKey, CaseIterable {
        case schema, machines
    }

    init(_ machines: [Machine]) {
        schema = MachineStateSchema()
        self.machines = machines.map(PersistedMachine.init)
    }

    init(from decoder: Decoder) throws {
        try requireExactKeys(decoder, Set(CodingKeys.allCases.map(\.rawValue)))
        let container = try decoder.container(keyedBy: CodingKeys.self)
        schema = try container.decode(MachineStateSchema.self, forKey: .schema)
        machines = try container.decode([PersistedMachine].self, forKey: .machines)
    }
}

/// MachineStore persists the paired-machine roster as JSON in an injected
/// directory using one exact, fingerprinted v1 envelope. Production points it
/// at Application Support via `defaultDirectory`; tests point it at a temp
/// directory so they never touch the real store.
public struct MachineStore: Sendable {
    private let fileURL: URL

    /// Creates a store whose `machines.json` lives in `directory`.
    public init(directory: URL) {
        fileURL = directory.appendingPathComponent("machines.json")
    }

    /// defaultDirectory is `Application Support/com.yasyf.cc-runtime`, created if
    /// absent.
    public static func defaultDirectory() throws -> URL {
        let base = try FileManager.default.url(
            for: .applicationSupportDirectory,
            in: .userDomainMask,
            appropriateFor: nil,
            create: true
        )
        let directory = base.appendingPathComponent("com.yasyf.cc-runtime", isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return directory
    }

    /// load reads the roster, returning an empty list when nothing has been saved.
    public func load() throws -> [Machine] {
        guard FileManager.default.fileExists(atPath: fileURL.path) else { return [] }
        let data = try Data(contentsOf: fileURL)
        return try JSONDecoder().decode(MachineStateEnvelope.self, from: data).machines.map(\.machine)
    }

    /// save writes the roster atomically, creating the directory if needed.
    public func save(_ machines: [Machine]) throws {
        try FileManager.default.createDirectory(
            at: fileURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        let data = try encoder.encode(MachineStateEnvelope(machines))
        try data.write(to: fileURL, options: .atomic)
    }

    /// remove drops the machine with `id` from the roster, leaving its Keychain
    /// token untouched. It is the roster half of `forget`.
    public func remove(id: String) throws {
        var machines = try load()
        machines.removeAll { $0.id == id }
        try save(machines)
    }

    /// forget removes a machine entirely: its roster entry and its Keychain token.
    public func forget(id: String) throws {
        try remove(id: id)
        try TokenStore.deleteToken(machineID: id)
    }
}

/// KeychainError wraps a non-success Security-framework OSStatus.
public struct KeychainError: Error, Equatable {
    public let status: OSStatus
    public init(status: OSStatus) {
        self.status = status
    }
}

/// TokenStoreError reports a token that is absent from the v1 device-bound
/// Keychain identity. Older or differently accessible secrets are never read or
/// rewritten; transfer the token explicitly or forget and re-pair the machine.
public enum TokenStoreError: Error, Equatable, LocalizedError {
    case repairRequired(machineID: String)

    public var errorDescription: String? {
        switch self {
        case let .repairRequired(machineID):
            "No v1 device-bound token is available for machine \(machineID). " +
                "Transfer its bearer token manually, or forget and re-pair the machine."
        }
    }
}

/// TokenStore keeps a machine's bearer token in the Keychain as a generic-password
/// item under service `com.yasyf.cc-runtime`, keyed by the machine id. The calls
/// are the portable SecItem surface, so they compile on macOS and iOS alike.
public enum TokenStore {
    /// service is the Keychain service every token item shares.
    public static let service = "com.yasyf.cc-runtime.token.v1"

    static let accessibility = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly as String

    /// setToken writes (replacing any prior value) the token for `machineID`, pinned to
    /// the device-only accessibility class so it never migrates onto another device via
    /// backup or restore and never syncs to iCloud, while a background daemon read still
    /// succeeds after the device's first post-boot unlock.
    public static func setToken(_ token: String, machineID: String) throws {
        let changes: [String: Any] = [
            kSecValueData as String: Data(token.utf8),
            kSecAttrAccessible as String: accessibility,
        ]
        let updateStatus = SecItemUpdate(baseQuery(machineID) as CFDictionary, changes as CFDictionary)
        if updateStatus == errSecSuccess {
            return
        }
        guard updateStatus == errSecItemNotFound else { throw KeychainError(status: updateStatus) }
        var attributes = baseQuery(machineID)
        attributes.merge(changes) { _, new in new }
        let addStatus = SecItemAdd(attributes as CFDictionary, nil)
        if addStatus == errSecSuccess {
            return
        }
        guard addStatus == errSecDuplicateItem else { throw KeychainError(status: addStatus) }
        let retryStatus = SecItemUpdate(baseQuery(machineID) as CFDictionary, changes as CFDictionary)
        if retryStatus == errSecSuccess {
            return
        }
        if retryStatus == errSecItemNotFound {
            throw TokenStoreError.repairRequired(machineID: machineID)
        }
        throw KeychainError(status: retryStatus)
    }

    /// token reads the exact v1 device-bound token for `machineID`.
    public static func token(machineID: String) throws -> String {
        var query = baseQuery(machineID)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound {
            throw TokenStoreError.repairRequired(machineID: machineID)
        }
        guard status == errSecSuccess else { throw KeychainError(status: status) }
        guard let data = result as? Data else {
            throw KeychainError(status: errSecInternalError)
        }
        guard let token = String(data: data, encoding: .utf8) else {
            throw KeychainError(status: errSecDecode)
        }
        return token
    }

    /// deleteToken removes the token for `machineID`; a missing item is not an error.
    public static func deleteToken(machineID: String) throws {
        let status = SecItemDelete(baseQuery(machineID) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeychainError(status: status)
        }
    }

    private static func baseQuery(_ machineID: String) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: machineID,
        ]
    }
}
