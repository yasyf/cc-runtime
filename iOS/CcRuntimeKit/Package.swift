// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "CcRuntimeKit",
    platforms: [
        .iOS(.v26),
        .macOS(.v26),
    ],
    products: [
        .library(name: "CcRuntimeKit", targets: ["CcRuntimeKit"]),
    ],
    targets: [
        .target(name: "CcRuntimeKit"),
        .testTarget(
            name: "CcRuntimeKitTests",
            dependencies: ["CcRuntimeKit"]
        ),
    ]
)
