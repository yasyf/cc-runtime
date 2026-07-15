@testable import CcRuntimeApp
import Testing

@MainActor
@Test func appRootConstructs() {
    _ = MachinesView(registry: InMemoryMachineRegistry(), probe: { _, _ in false })
}
