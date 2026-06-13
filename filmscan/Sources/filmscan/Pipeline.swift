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
    var diarize: Bool = true
    var diarModels: String? = nil

    func run() async throws {
        // 16 kHz mono audio is needed by both transcribe and diarize; decode once,
        // on demand (skipped entirely when both their artifacts are cached).
        var samplesCache: [Float]? = nil
        func loadSamples() async throws -> [Float] {
            if let s = samplesCache { return s }
            log("demux: decoding audio → 16kHz mono …")
            let s = try await Demux.audioSamples16k(videoURL: videoURL)
            log("demux: \(s.count) samples (~\(s.count / 16000)s)")
            samplesCache = s
            return s
        }

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
                let samples = try await loadSamples()
                log("transcribe: WhisperKit \(model) …")
                let t = try await Transcribe.run(videoURL: videoURL, samples: samples, lang: lang, model: model, modelFolder: modelFolder, compute: compute)
                try saveJSON(t, to: transcriptURL)
                log("transcribe: \(t.segments.count) segments")
                transcript = t
            } catch {
                log("transcribe: FAILED (\(error.localizedDescription)) — skipping subtitles; keyframes+faces are done.")
            }
        }

        // ── diarize (audio "who-spoke-when"; needs FluidAudio models; tolerant) ──
        var audioTurns: [AudioTurn] = []
        if diarize, transcript != nil {
            let diarURL = outDir.appendingPathComponent("diarize.json")
            if let cached = try? loadJSON(Diarization.self, from: diarURL) {
                log("diarize: cached (\(cached.turns.count) turns)")
                audioTurns = cached.turns
            } else {
                do {
                    let samples = try await loadSamples()
                    log("diarize: FluidAudio …")
                    let turns = try await Diarize.run(samples: samples, modelsDir: diarModels, compute: compute)
                    try saveJSON(Diarization(turns: turns), to: diarURL)
                    let speakers = Set(turns.map { $0.speaker }).count
                    log("diarize: \(turns.count) turns / \(speakers) speakers")
                    audioTurns = turns
                } catch {
                    log("diarize: FAILED (\(error.localizedDescription)) — visual-only fusion.")
                }
            }
        }

        // ── fuse: audio backbone × visual active-speaker → per-line attribution ──
        var final = transcript
        if let t = transcript {
            let fusedURL = outDir.appendingPathComponent("fused.json")
            if let cached = try? loadJSON(Transcript.self, from: fusedURL) {
                log("fuse: cached")
                final = cached
            } else {
                log("fuse: \(audioTurns.isEmpty ? "visual-only" : "audio+visual") attribution …")
                let f = try await Fuse.run(videoURL: videoURL, outDir: outDir, transcript: t, audioTurns: audioTurns)
                try saveJSON(f, to: fusedURL)
                let n = f.segments.filter { !$0.speakerKey.isEmpty }.count
                log("fuse: \(n)/\(f.segments.count) lines attributed to a speaker")
                final = f
            }
        }

        // ── emit (only if we have a transcript) ─────────────────────
        if let t = final {
            let stem = videoURL.deletingPathExtension().lastPathComponent
            let srtURL = outDir.appendingPathComponent("\(stem).srt")
            try Emit.srt(t, to: srtURL)
            log("emit: \(srtURL.path)")
        }
    }
}
