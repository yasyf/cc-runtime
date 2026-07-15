import SwiftUI

/// CcRuntimeApp is the app entry point. The screens — the machine roster, the
/// session list, and the answer surface — land in a later stage; for now the single
/// scene roots an empty ContentView so the app builds and launches.
@main
struct CcRuntimeApp: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
}
