// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "AiUsage",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "AiUsage", targets: ["AiUsage"])
    ],
    targets: [
        .executableTarget(name: "AiUsage"),
        .testTarget(name: "AiUsageTests", dependencies: ["AiUsage"])
    ],
    swiftLanguageModes: [.v5]
)
