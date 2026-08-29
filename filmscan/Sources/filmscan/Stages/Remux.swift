import Foundation
import AVFoundation
import CoreMedia

// AVFoundation (the engine behind every other stage) can't open Matroska/WebM and
// a handful of other containers on macOS. This stage detects those and remuxes the
// input to a temp .mp4 with ffmpeg *before* the pipeline runs, so keyframes / audio
// / fuse all see a file AVFoundation understands. Lossless stream-copy when the
// codecs are already mp4-compatible (H.264/HEVC + AAC — the common case for .mkv),
// transcode fallback otherwise. The original file is never touched and naming
// (e.g. the .srt) still uses the original stem.
enum Remux {
    // Containers AVFoundation can't open on macOS → need an ffmpeg remux first.
    static let needsRemux: Set<String> = [
        "mkv", "webm", "avi", "flv", "ts", "m2ts", "mts", "ogv", "ogg",
        "wmv", "mpg", "mpeg", "vob", "rmvb", "rm", "divx", "asf",
    ]

    static func locateFFmpeg() -> String? {
        let candidates = ["/opt/homebrew/bin/ffmpeg", "/usr/local/bin/ffmpeg", "/usr/bin/ffmpeg"]
        for c in candidates where FileManager.default.isExecutableFile(atPath: c) { return c }
        if let r = try? runProcess("/usr/bin/which", ["ffmpeg"]) {
            let p = r.output.trimmingCharacters(in: .whitespacesAndNewlines)
            if !p.isEmpty, FileManager.default.isExecutableFile(atPath: p) { return p }
        }
        return nil
    }

    /// Returns a video URL AVFoundation can read. Pass-through for mp4/mov/etc;
    /// for an unsupported container, remux to `<outDir>/_remux.mp4` (cached across
    /// re-runs) and return that.
    static func ensurePlayable(_ input: URL, outDir: URL) async throws -> URL {
        let ext = input.pathExtension.lowercased()
        guard needsRemux.contains(ext) else { return input }

        let outMp4 = outDir.appendingPathComponent("_remux.mp4")
        if FileManager.default.fileExists(atPath: outMp4.path),
           let attrs = try? FileManager.default.attributesOfItem(atPath: outMp4.path),
           (attrs[.size] as? Int ?? 0) > 0 {
            log("remux: cached \(outMp4.lastPathComponent)")
            return outMp4
        }
        guard let ff = locateFFmpeg() else {
            throw Err("\(ext) needs ffmpeg to remux, but ffmpeg was not found — `brew install ffmpeg`")
        }
        try? FileManager.default.removeItem(at: outMp4)

        // 1) fast path: lossless stream-copy into mp4 (H.264/HEVC + AAC). ffmpeg will
        //    happily copy codecs mp4 *can* carry but AVFoundation *can't decode*
        //    (e.g. VP9-in-mp4), so we don't trust a 0 exit — we probe the result with
        //    AVFoundation (decode one frame) and only keep it if that succeeds.
        log("remux: \(ext) → mp4 (stream copy) …")
        let copyArgs = [
            "-y", "-i", input.path, "-map", "0:v:0?", "-map", "0:a:0?",
            "-c", "copy", "-movflags", "+faststart", outMp4.path,
        ]
        if let r = try? runProcess(ff, copyArgs), r.status == 0, fileNonEmpty(outMp4),
           await avDecodable(outMp4) {
            log("remux: stream-copy ok → \(outMp4.lastPathComponent)")
            return outMp4
        }

        // 2) fallback: codec mp4-incompatible OR not AVFoundation-decodable
        //    (VP9/AV1/VP8 video, etc.) → transcode video→h264, audio→aac.
        try? FileManager.default.removeItem(at: outMp4)
        log("remux: stream-copy not usable; transcoding → h264/aac (slower) …")
        let txArgs = [
            "-y", "-i", input.path, "-map", "0:v:0?", "-map", "0:a:0?",
            "-c:v", "libx264", "-preset", "veryfast", "-crf", "20",
            "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", outMp4.path,
        ]
        let r = try runProcess(ff, txArgs)
        guard r.status == 0, fileNonEmpty(outMp4) else {
            throw Err("ffmpeg remux of \(input.lastPathComponent) failed (exit \(r.status))")
        }
        log("remux: transcode ok → \(outMp4.lastPathComponent)")
        return outMp4
    }

    private static func fileNonEmpty(_ url: URL) -> Bool {
        guard let attrs = try? FileManager.default.attributesOfItem(atPath: url.path) else { return false }
        return (attrs[.size] as? Int ?? 0) > 0
    }

    /// True iff AVFoundation can open the file AND actually decode a video frame —
    /// the real bar, since the downstream stages (keyframes/fuse) decode frames.
    private static func avDecodable(_ url: URL) async -> Bool {
        let asset = AVURLAsset(url: url)
        guard let tracks = try? await asset.loadTracks(withMediaType: .video), !tracks.isEmpty else {
            return false
        }
        let gen = AVAssetImageGenerator(asset: asset)
        gen.requestedTimeToleranceBefore = .positiveInfinity
        gen.requestedTimeToleranceAfter = .positiveInfinity
        do {
            _ = try await gen.image(at: CMTime(seconds: 0, preferredTimescale: 600)).image
            return true
        } catch {
            return false
        }
    }
}
