// swift-tools-version: 6.0
import PackageDescription

// filmscan — Apple-native video analyzer for polar-film. Produces
// speaker-attributed subtitles + keyframes from a video file. macOS CLI;
// the whole pipeline is AVFoundation / WhisperKit / Vision / CoreML + FluidAudio
// diarization, so it ships as one self-contained binary (no Python runtime).
//
// Swift 6 is required by FluidAudio. Build hosts must use Xcode 16+/Swift 6 (the
// old Swift-5.10 on-box build retires); the prebuilt binary still targets
// macOS 14 and runs on those hosts.
let package = Package(
    name: "filmscan",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(url: "https://github.com/apple/swift-argument-parser.git", from: "1.3.0"),
        // Pinned: newer WhisperKit pulls swift-transformers 1.1 → swift-jinja 2.0.
        // 0.9.0 is known-good with our toolchain; keep it pinned to avoid churn.
        .package(url: "https://github.com/argmaxinc/WhisperKit.git", .exact("0.9.0")),
        // On-device speaker diarization (pre-converted CoreML pyannote + wespeaker).
        .package(url: "https://github.com/FluidInference/FluidAudio.git", from: "0.12.4"),
    ],
    targets: [
        .executableTarget(
            name: "filmscan",
            dependencies: [
                .product(name: "ArgumentParser", package: "swift-argument-parser"),
                .product(name: "WhisperKit", package: "WhisperKit"),
                .product(name: "FluidAudio", package: "FluidAudio"),
            ],
            path: "Sources/filmscan",
            // tools-version 6.0 is required to resolve FluidAudio, but our own code
            // stays in Swift 5 language mode — avoids strict-concurrency churn on
            // ArgumentParser's non-Sendable CommandConfiguration statics.
            swiftSettings: [.swiftLanguageMode(.v5)]
        )
    ]
)
