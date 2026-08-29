import ArgumentParser
import AVFoundation
import Foundation

// `filmscan extract` — the cheap, ML-free first stage of the segmented fleet
// pipeline. Does everything `analyze` does EXCEPT transcribe/diarize/fuse/emit:
//   remux (if needed) → keyframes → faces → export audio.m4a
// None of this needs the Neural Engine, so it runs fine on cheap x86 boxes. The
// heavy ANE work (WhisperKit/FluidAudio) is the separate `analyze-audio` stage,
// which consumes only the small audio.m4a this produces — so the two stages can
// live on different machines (x86 extract → arm64+ANE analyze). See
// doc/arch/task-processing-v2.md.
struct Extract: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Stage 1 (x86-friendly): keyframes + faces + audio.m4a. No transcription."
    )

    @Argument(help: "Path to the video file. mkv/webm/avi/… auto-remuxed to mp4 via ffmpeg.")
    var video: String

    @Option(name: .long, help: "Keyframe sampling interval in seconds.")
    var frameInterval: Double = 2.0

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

        // AVFoundation can't open .mkv/.webm/etc → remux to a temp mp4 first.
        let mediaURL = try await Remux.ensurePlayable(videoURL, outDir: outDir)

        // ── keyframes (offline, AVFoundation) ───────────────────────
        let framesURL = outDir.appendingPathComponent("frames.json")
        let frames: Frames
        if let cached = try? loadJSON(Frames.self, from: framesURL) {
            log("keyframes: cached (\(cached.frames.count) frames)")
            frames = cached
        } else {
            log("keyframes: sampling every \(frameInterval)s …")
            frames = try await Keyframes.run(videoURL: mediaURL, outDir: outDir, everySec: frameInterval)
            try saveJSON(frames, to: framesURL)
            log("keyframes: \(frames.frames.count) frames")
        }

        // ── faces (offline, Vision) ─────────────────────────────────
        let facesURL = outDir.appendingPathComponent("faces.json")
        var faceCount = 0
        if let cached = try? loadJSON(Faces.self, from: facesURL) {
            log("faces: cached (\(cached.faces.count) faces, \(cached.clusterCount) clusters)")
            faceCount = cached.faces.count
        } else {
            log("faces: detecting + clustering …")
            let faces = try FacesStage.run(outDir: outDir, frames: frames)
            try saveJSON(faces, to: facesURL)
            log("faces: \(faces.faces.count) faces → \(faces.clusterCount) clusters")
            faceCount = faces.faces.count
        }

        // ── audio export (AAC m4a) — the only thing the ANE stage needs ──
        let audioURL = outDir.appendingPathComponent("audio.m4a")
        if FileManager.default.fileExists(atPath: audioURL.path),
           let attrs = try? FileManager.default.attributesOfItem(atPath: audioURL.path),
           (attrs[.size] as? Int ?? 0) > 0 {
            log("audio: cached audio.m4a")
        } else {
            log("audio: exporting AAC audio.m4a …")
            try await exportAudioM4A(from: mediaURL, to: audioURL)
            let mb = (try? FileManager.default.attributesOfItem(atPath: audioURL.path)[.size] as? Int ?? 0).flatMap { $0 } ?? 0
            log("audio: audio.m4a (\(mb / 1024 / 1024) MB)")
        }

        // ── manifest (what the analyze stage / agent reads) ─────────
        let manifest = ExtractManifest(media: mediaName, audio: "audio.m4a",
                                       frames: frames.frames.count, faces: faceCount)
        try saveJSON(manifest, to: outDir.appendingPathComponent("extract-manifest.json"))
        log("extract: done → \(outDir.path)")
    }

    /// Export the asset's audio track to a small AAC .m4a (drops video). The
    /// analyze stage decodes this back to 16 kHz mono for Whisper/FluidAudio.
    private func exportAudioM4A(from src: URL, to dst: URL) async throws {
        try? FileManager.default.removeItem(at: dst)
        let asset = AVURLAsset(url: src)
        guard let session = AVAssetExportSession(asset: asset, presetName: AVAssetExportPresetAppleM4A) else {
            throw Err("cannot create audio export session")
        }
        session.outputURL = dst
        session.outputFileType = .m4a
        await session.export()
        guard session.status == .completed else {
            throw Err("audio export failed: \(session.error?.localizedDescription ?? "status \(session.status.rawValue)")")
        }
    }
}

/// Small descriptor written by `extract`, read by `analyze-audio` / the agent.
struct ExtractManifest: Codable {
    var media: String
    var audio: String
    var frames: Int
    var faces: Int
}
