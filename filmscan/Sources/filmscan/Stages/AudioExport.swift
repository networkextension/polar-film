import Foundation
import AVFoundation

// Extract the video's audio track to a real .m4a (AAC) file for upload to the
// workspace music library. This is distinct from Demux, which decodes raw 16 kHz
// Float samples for WhisperKit/FluidAudio — here we want a shareable, playable
// container, not PCM.
//
// AVAssetExportSession with the AppleM4A preset emits an audio-only .m4a, so it
// strips the video automatically. Pure AVFoundation, so it runs on x86 too.
enum AudioExport {
    struct Result {
        let url: URL
        let durationMs: Int
    }

    /// Export the first audio track of `videoURL` to `<outDir>/audio.m4a`.
    /// Returns nil if the video has no audio track.
    static func run(videoURL: URL, outDir: URL) async throws -> Result? {
        let asset = AVURLAsset(url: videoURL)
        let tracks = try await asset.loadTracks(withMediaType: .audio)
        guard !tracks.isEmpty else { return nil }

        let outURL = outDir.appendingPathComponent("audio.m4a")
        // Idempotent: reuse a prior export so re-runs are cheap (mirrors the
        // pipeline's artifact-skip discipline).
        if FileManager.default.fileExists(atPath: outURL.path) {
            let dur = CMTimeGetSeconds(try await asset.load(.duration))
            log("audio: cached \(outURL.lastPathComponent)")
            return Result(url: outURL, durationMs: Int((dur * 1000).rounded()))
        }
        try? FileManager.default.removeItem(at: outURL)

        guard let session = AVAssetExportSession(asset: asset, presetName: AVAssetExportPresetAppleM4A) else {
            throw Err("could not create audio export session")
        }
        session.outputURL = outURL
        session.outputFileType = .m4a

        // AVAssetExportSession.export() (the async API) is macOS 15+. Bridge the
        // legacy completion-handler API so we keep the macOS 14 deployment target.
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            session.exportAsynchronously {
                switch session.status {
                case .completed:
                    cont.resume()
                case .failed, .cancelled:
                    cont.resume(throwing: session.error ?? Err("audio export \(session.status.rawValue)"))
                default:
                    cont.resume(throwing: Err("audio export ended in status \(session.status.rawValue)"))
                }
            }
        }

        let dur = CMTimeGetSeconds(try await asset.load(.duration))
        return Result(url: outURL, durationMs: Int((dur * 1000).rounded()))
    }
}
