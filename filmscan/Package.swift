// swift-tools-version: 5.9
import PackageDescription

// filmscan — Apple-native video analyzer for polar-film. Produces
// speaker-attributed subtitles + keyframes from a video file. macOS CLI;
// the whole pipeline is AVFoundation / WhisperKit / Vision / CoreML, so it
// ships as one self-contained binary (no Python runtime).
let package = Package(
    name: "filmscan",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.3.0"),
        // Pinned: newer WhisperKit pulls swift-transformers 1.1 → swift-jinja 2.0,
        // which needs Swift 6.0. Build hosts on Xcode/Swift 5.10 (macOS 14) can't
        // resolve that, so pin to a 5.x-compatible WhisperKit.
        .package(url: "https://github.com/argmaxinc/WhisperKit.git", .exact("0.9.0")),
    ],
    targets: [
        .executableTarget(
            name: "filmscan",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .product(name: "WhisperKit", package: "WhisperKit"),
            ],
            path: "Sources/filmscan"
        )
    ]
)
