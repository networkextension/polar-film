import ArgumentParser
import AVFoundation
import Foundation

// `filmscan extract` — the x86-capable "basic data extraction" tier. Runs the
// offline AVFoundation + Vision stages (no WhisperKit/FluidAudio), uploads the
// outputs into the shared workspace libraries, and emits a handoff manifest:
//   • audio track   → workspace music library  (POST /api/tracks)
//   • keyframes      → workspace photo library  (POST /api/photo/assets/upload)
//   • extract-manifest.json (audio track id, keyframe asset ids, face boxes+embeddings)
//
// Because nothing here needs the Neural Engine, any online agent — x86 or arm64 —
// can run it, so extraction never stalls waiting for a particular architecture.
// The ANE-preferred `analyze` stage consumes the manifest.
struct Extract: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Extract audio + keyframes/faces from a video and upload them to the workspace music/photo libraries (x86-capable)."
    )

    @Argument(help: "Path to the video file (mp4/mov).")
    var video: String

    @Option(name: .long, help: "Dock base URL (or env FILMSCAN_SERVER), e.g. https://zen.4950.store:2443.")
    var server: String?

    @Option(name: .long, help: "Bearer access token (or env FILMSCAN_TOKEN).")
    var token: String?

    @Option(name: .long, help: "Workspace id (X-Workspace-Id); omit for personal.")
    var workspaceID: String?

    @Option(name: .long, help: "Film movie id — keyframes upload to its screenshots (central assets) for identity face refs.")
    var mediaID: String?

    @Option(name: .long, help: "Keyframe sampling interval in seconds.")
    var frameInterval: Double = 2.0

    @Option(name: .shortAndLong, help: "Output directory (default: alongside the video).")
    var out: String?

    @Flag(name: .long, help: "Skip uploading the audio to the music library.")
    var noAudio: Bool = false

    @Flag(name: .long, help: "Skip uploading keyframes to the photo library.")
    var noKeyframes: Bool = false

    func run() async throws {
        let videoURL = URL(fileURLWithPath: video)
        guard FileManager.default.fileExists(atPath: videoURL.path) else {
            throw ValidationError("video not found: \(video)")
        }
        let base = (server ?? ProcessInfo.processInfo.environment["FILMSCAN_SERVER"] ?? "")
            .trimmingCharacters(in: .whitespaces)
        guard let baseURL = URL(string: base), base.hasPrefix("http") else {
            throw ValidationError("--server (or FILMSCAN_SERVER) must be an http(s) URL")
        }
        let tok = (token ?? ProcessInfo.processInfo.environment["FILMSCAN_TOKEN"] ?? "")
            .trimmingCharacters(in: .whitespaces)
        guard !tok.isEmpty else { throw ValidationError("--token (or FILMSCAN_TOKEN) is required") }

        let mediaName = videoURL.deletingPathExtension().lastPathComponent
        let baseDir = out.map { URL(fileURLWithPath: $0) } ?? videoURL.deletingLastPathComponent()
        let outDir = baseDir.appendingPathComponent(mediaName + ".filmscan", isDirectory: true)
        try FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)

        // ── audio → music library ───────────────────────────────────
        var audioTrackID: String? = nil
        var audioAssetID: Int64? = nil
        var audioDurationMs = 0
        if !noAudio {
            log("audio: exporting .m4a …")
            if let res = try await AudioExport.run(videoURL: videoURL, outDir: outDir) {
                audioDurationMs = res.durationMs
                let music = MusicClient(base: baseURL, token: tok, workspaceID: workspaceID)
                log("audio: uploading to music library …")
                let (tid, aid) = try await music.uploadTrack(fileURL: res.url, durationMs: res.durationMs, title: mediaName)
                audioTrackID = tid
                audioAssetID = aid
                log("audio: track \(audioTrackID ?? "?") asset \(audioAssetID.map(String.init) ?? "?") (\(audioDurationMs / 1000)s)")
            } else {
                log("audio: no audio track — skipping")
            }
        }

        // ── keyframes (offline) ─────────────────────────────────────
        let framesURL = outDir.appendingPathComponent("frames.json")
        let frames: Frames
        if let cached = try? loadJSON(Frames.self, from: framesURL) {
            log("keyframes: cached (\(cached.frames.count) frames)")
            frames = cached
        } else {
            log("keyframes: sampling every \(frameInterval)s …")
            frames = try await Keyframes.run(videoURL: videoURL, outDir: outDir, everySec: frameInterval)
            try saveJSON(frames, to: framesURL)
            log("keyframes: \(frames.frames.count) frames")
        }

        // ── faces (offline; Vision runs on x86) ─────────────────────
        let facesURL = outDir.appendingPathComponent("faces.json")
        let faces: Faces
        if let cached = try? loadJSON(Faces.self, from: facesURL) {
            log("faces: cached (\(cached.faces.count) faces, \(cached.clusterCount) clusters)")
            faces = cached
        } else {
            log("faces: detecting + clustering …")
            faces = try FacesStage.run(outDir: outDir, frames: frames)
            try saveJSON(faces, to: facesURL)
            log("faces: \(faces.faces.count) faces → \(faces.clusterCount) clusters")
        }

        // ── keyframes → film screenshots (central polar-assets; film owns them,
        //    has ts_ms + asset_id → identity face samples reference the frame) ──
        if !noKeyframes, !frames.frames.isEmpty, let mid = mediaID, !mid.isEmpty {
            let film = FilmClient(base: baseURL, token: tok, workspaceID: workspaceID, mediaID: mid)
            log("keyframes: uploading \(frames.frames.count) to film screenshots …")
            let r = try await film.uploadKeyframes(frames.frames, baseDir: outDir)
            log("keyframes: \(r.uploaded) uploaded, \(r.deduped) deduped → film screenshots")
        } else if !noKeyframes {
            log("keyframes: no --media-id → kept local (no upload)")
        }

        // ── emit manifest ───────────────────────────────────────────
        let media = try await probeMedia(videoURL: videoURL, durationMs: audioDurationMs)
        let manifest = ExtractManifest(
            media: media,
            workspaceID: workspaceID,
            audioTrackID: audioTrackID,
            audioAssetID: audioAssetID,
            audioDurationMs: audioDurationMs,
            frames: [],   // keyframes live in film screenshots (queried by ts_ms), not the manifest
            faces: faces.faces.map { ManifestFace(timeMs: $0.timeMs, box: $0.box, cluster: $0.cluster, embedding: $0.embedding) },
            clusterCount: faces.clusterCount
        )
        let manifestURL = outDir.appendingPathComponent("extract-manifest.json")
        try saveJSON(manifest, to: manifestURL)
        log("extract: done → \(manifestURL.path)")
    }

    /// Best-effort probe of the source video's shape for the manifest.
    private func probeMedia(videoURL: URL, durationMs: Int) async throws -> MediaInfo {
        var info = MediaInfo(path: videoURL.path, durationMs: durationMs)
        let asset = AVURLAsset(url: videoURL)
        if let dur = try? await asset.load(.duration) {
            let ms = Int((CMTimeGetSeconds(dur) * 1000).rounded())
            if ms > 0 { info.durationMs = ms }
        }
        if let track = try? await asset.loadTracks(withMediaType: .video).first {
            if let size = try? await track.load(.naturalSize) {
                info.width = Int(abs(size.width)); info.height = Int(abs(size.height))
            }
            if let rate = try? await track.load(.nominalFrameRate) { info.fps = Double(rate) }
        }
        return info
    }
}
