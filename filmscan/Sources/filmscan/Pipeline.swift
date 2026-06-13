import Foundation

// Runs the analysis stages. Each stage writes a JSON artifact under <outDir>/ and
// is skipped when that artifact exists, so re-runs are cheap (mirrors
// analyze_jobs.steps_json). Keyframes + faces are fully offline (AVFoundation /
// Vision); transcribe needs the Whisper model and is tolerant of failure so the
// visual stages still complete without it.
struct Pipeline {
    let videoURL: URL
    let outDir: URL
    let lang: String
    let model: String
    var modelFolder: String? = nil
    var frameIntervalSec: Double = 2.0
    var compute: String = "default"

    func run() async throws {
        // ── keyframes (offline) ─────────────────────────────────────
        let framesURL = outDir.appendingPathComponent("frames.json")
        let frames: Frames
        if let cached = try? loadJSON(Frames.self, from: framesURL) {
            log("keyframes: cached (\(cached.frames.count) frames)")
            frames = cached
        } else {
            log("keyframes: sampling every \(frameIntervalSec)s …")
            frames = try await Keyframes.run(videoURL: videoURL, outDir: outDir, everySec: frameIntervalSec)
            try saveJSON(frames, to: framesURL)
            log("keyframes: \(frames.frames.count) frames")
        }

        // ── faces (offline) ─────────────────────────────────────────
        let facesURL = outDir.appendingPathComponent("faces.json")
        if let cached = try? loadJSON(Faces.self, from: facesURL) {
            log("faces: cached (\(cached.faces.count) faces, \(cached.clusterCount) clusters)")
        } else {
            log("faces: detecting + clustering …")
            let faces = try FacesStage.run(outDir: outDir, frames: frames)
            try saveJSON(faces, to: facesURL)
            log("faces: \(faces.faces.count) faces → \(faces.clusterCount) clusters")
        }

        // ── transcribe (needs Whisper model; tolerant of failure) ───
        let transcriptURL = outDir.appendingPathComponent("transcript.json")
        var transcript: Transcript? = try? loadJSON(Transcript.self, from: transcriptURL)
        if let t = transcript {
            log("transcribe: cached (\(t.segments.count) segments)")
        } else {
            do {
                log("demux: decoding audio → 16kHz mono …")
                let samples = try await Demux.audioSamples16k(videoURL: videoURL)
                log("demux: \(samples.count) samples (~\(samples.count / 16000)s)")
                log("transcribe: WhisperKit \(model) …")
                let t = try await Transcribe.run(videoURL: videoURL, samples: samples, lang: lang, model: model, modelFolder: modelFolder, compute: compute)
                try saveJSON(t, to: transcriptURL)
                log("transcribe: \(t.segments.count) segments")
                transcript = t
            } catch {
                log("transcribe: FAILED (\(error.localizedDescription)) — skipping subtitles; keyframes+faces are done.")
            }
        }

        // ── emit (only if we have a transcript) ─────────────────────
        if let t = transcript {
            let stem = videoURL.deletingPathExtension().lastPathComponent
            let srtURL = outDir.appendingPathComponent("\(stem).srt")
            try Emit.srt(t, to: srtURL)
            log("emit: \(srtURL.path)")
        }

        // P2 : Diarize + Fuse → attribute each subtitle line to a face cluster.
    }
}
