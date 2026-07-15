// The persistence seam the pairing flow writes through and the roster reads from:
// the Machine record in the JSON store and its bearer token in the Keychain, moved
// as one unit. KeychainMachineRegistry is the production conformer; tests inject an
// in-memory fake so no filesystem or Keychain is touched.

import CcRuntimeKit
import Foundation

/// MachineRegistry couples the roster store and the token store so a machine and its
/// secret are added and removed together.
protocol MachineRegistry: Sendable {
    func load() throws -> [Machine]
    func add(_ machine: Machine, token: String) throws
    func remove(_ machine: Machine) throws
    func token(for machineID: String) throws -> String?
}

/// KeychainMachineRegistry stores the roster in Application Support and each token in
/// the Keychain, keeping the secret out of the JSON record.
struct KeychainMachineRegistry: MachineRegistry {
    func load() throws -> [Machine] {
        try store().load()
    }

    func add(_ machine: Machine, token: String) throws {
        try TokenStore.setToken(token, machineID: machine.id)
        var machines = try store().load()
        machines.removeAll { $0.id == machine.id }
        machines.append(machine)
        try store().save(machines)
    }

    func remove(_ machine: Machine) throws {
        try store().forget(id: machine.id)
    }

    func token(for machineID: String) throws -> String? {
        try TokenStore.token(machineID: machineID)
    }

    private func store() throws -> MachineStore {
        try MachineStore(directory: MachineStore.defaultDirectory())
    }
}
