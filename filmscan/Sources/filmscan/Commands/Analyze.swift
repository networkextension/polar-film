import ArgumentParser
import Foundation

struct Analyze: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Analyze a video → speaker-attributed subtitles + keyframes."
    )

    @Argument(help: "Path to the video file (mp4/mov; mkv needs demux — P1).")
    var video: String

    @Option(name: .shortAndLong, help: "Spoken language code.")
    var lang: String = "en"

    @Option(name: .long, help: "Whisper model (base.en, small.en, …).")
    var model: String = "base.en"

    @Option(name: .long, help: "Path to a pre-downloaded WhisperKit model folder (skips the HF download).")
    var modelFolder: String?

    @Option(name: .long, help: "Keyframe sampling interval in seconds.")
    var frameInterval: Double = 2.0

    @Option(name: .long, help: "CoreML compute units: default | cpu | cpuAndGPU | cpuAndNeuralEngine | all. Use 'cpu' if transcription comes out garbled (macOS 14.0 ANE bug).")
    var compute: String = "default"

    @Option(name: .shortAndLong, help: "Output directory (default: alongside the video).")
    var out: String?

    func run() async throws {
        let videoURL = URL(fileURLWithPath: video)
        guard FileManager.default.fileExists(atPath: videoURL.path) else {
            throw ValidationError("video not found: \(video)")
        }
        let mediaName = videoURL.deletingPathExtension().lastPathComponent
        let base = out.map { URL(fileURLWithPath: $0) } ?? videoURL.deletingLastPathComponent()
        let outDir = base.appendingPathComponent(mediaName + ".filmscan", isDirectory: true)
        try FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)

        let pipeline = Pipeline(videoURL: videoURL, outDir: outDir, lang: lang, model: model,
                                modelFolder: modelFolder, frameIntervalSec: frameInterval, compute: compute)
        try await pipeline.run()
    }
}
