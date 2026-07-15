import SwiftUI

/// CcRuntimeApp is the app entry point. It roots the navigation at MachinesView and
/// shares the AppDelegate's PushCenter down the tree so the connected machine screen
/// can register this device's APNs token and route a tapped notification.
@main
struct CcRuntimeApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        WindowGroup {
            MachinesView()
                .environment(appDelegate.push)
        }
    }
}
