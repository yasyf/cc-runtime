import SwiftUI

/// ContentView is the placeholder root view. The real navigation stack — pairing,
/// the machine roster, sessions, and the answer surface — replaces it in the
/// screens stage.
struct ContentView: View {
    var body: some View {
        ContentUnavailableView(
            "cc-runtime",
            systemImage: "bell.badge",
            description: Text("Pair a daemon to answer questions and receive notifications.")
        )
    }
}

#Preview {
    ContentView()
}
