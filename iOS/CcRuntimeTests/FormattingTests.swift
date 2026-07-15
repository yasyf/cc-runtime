@testable import CcRuntimeApp
import Foundation
import Testing

@Test func scopeLabelTakesLastPathSegment() {
    #expect(scopeLabel("/Users/me/Code/cc-runtime") == "cc-runtime")
    #expect(scopeLabel("/Users/me/Code/cc-runtime/") == "cc-runtime")
    #expect(scopeLabel("solo") == "solo")
    #expect(scopeLabel("") == "session")
}

@Test func diffLineKindClassesLines() {
    #expect(diffLineKind("+added") == .addition)
    #expect(diffLineKind("+++ b/file") == .context)
    #expect(diffLineKind("-removed") == .deletion)
    #expect(diffLineKind("--- a/file") == .context)
    #expect(diffLineKind("@@ -1,2 +1,3 @@") == .hunk)
    #expect(diffLineKind(" unchanged") == .context)
}
