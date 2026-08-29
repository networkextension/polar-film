import ArgumentParser
import Foundation

// `filmscan analyze-audio` — stage 2 of the segmented fleet pipeline. Consumes
// ONLY the small audio.m4a produced by `extract` (no video needed) and runs the
// ANE-bound work: WhisperKit transcribe → FluidAudio diarize → audio-only speaker
// attribution → emit SRT. This is the piece that wants an Apple-Silicon box with
// a Neural Engine; it's deliberately decoupled from the cheap x86 `extract` so the
// two can run on different machines. Visual fusion (active-speaker from frames) is
// intentionally skipped here — that needs the video and is the all-in-one
// `analyze`'s job; attribution here comes from audio diarization alone.
struct AnalyzeAudio: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "analyze-audio",
        abstract: "Stage 2 (ANE): transcribe + diarize an extracted audio file → SRT."
    )

    @Argument(help: "Path to the audio file (audio.m4a from `extract`, or any AVFoundation-readable audio).")
    var audio: String

    @Option(name: .long, help: "Output stem for the .srt (e.g. the movie title). Default: the audio file's name.")
    var name: String?

    @Option(name: .shortAndLong, help: "Spoken language code.")
    var lang: String = "en"

    @Option(name: .long, help: "Whisper model (base.en, small.en, …).")
    var model: String = "base.en"

    @Option(name: .long, help: "Path to a pre-downloaded WhisperKit model folder (skips the HF download).")
    var modelFolder: String?

    @Option(name: .long, help: "CoreML compute units: default | cpu | cpuAndGPU | cpuAndNeuralEngine | all.")
    var compute: String = "default"

    @Flag(name: .long, inversion: .prefixedNo, help: "Audio speaker diarization. On by default.")
    var diarize: Bool = true

    @Option(name: .long, help: "Folder with FluidAudio CoreML models; omit to auto-download.")
    var diarModels: String?

    @Option(name: .shortAndLong, help: "Output directory (default: alongside the audio).")
    var out: String?

    func run() async throws {
        let audioURL = URL(fileURLWithPath: audio)
        guard FileManager.default.fileExists(atPath: audioURL.path) else {
            throw ValidationError("audio not found: \(audio)")
        }
        let stem = name ?? audioURL.deletingPathExtension().lastPathComponent
        let base = out.map { URL(fileURLWithPath: $0) } ?? audioURL.deletingLastPathComponent()
        let outDir = base.appendingPathComponent(stem + ".filmscan", isDirectory: true)
        try FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)

        log("demux: decoding audio → 16kHz mono …")
        let samples = try await Demux.audioSamples16k(videoURL: audioURL)
        log("demux: \(samples.count) samples (~\(samples.count / 16000)s)")
        guard !samples.isEmpty else { throw Err("no audio samples decoded from \(audio)") }

        // ── transcribe (ANE/Whisper) ────────────────────────────────
        let transcriptURL = outDir.appendingPathComponent("transcript.json")
        var transcript: Transcript
        if let cached = try? loadJSON(Transcript.self, from: transcriptURL) {
            log("transcribe: cached (\(cached.segments.count) segments)")
            transcript = cached
        } else {
            log("transcribe: WhisperKit \(model) …")
            transcript = try await Transcribe.run(videoURL: audioURL, samples: samples, lang: lang,
                                                  model: model, modelFolder: modelFolder, compute: compute)
            try saveJSON(transcript, to: transcriptURL)
            log("transcribe: \(transcript.segments.count) segments")
        }

        // ── diarize (audio who-spoke-when; best-effort) ─────────────
        var turns: [AudioTurn] = []
        if diarize {
            let diarURL = outDir.appendingPathComponent("diarize.json")
            if let cached = try? loadJSON(Diarization.self, from: diarURL) {
                log("diarize: cached (\(cached.turns.count) turns)")
                turns = cached.turns
            } else {
                do {
                    log("diarize: FluidAudio …")
                    turns = try await Diarize.run(samples: samples, modelsDir: diarModels, compute: compute)
                    try saveJSON(Diarization(turns: turns), to: diarURL)
                    let speakers = Set(turns.map { $0.speaker }).count
                    log("diarize: \(turns.count) turns / \(speakers) speakers")
                } catch {
                    log("diarize: FAILED (\(error.localizedDescription)) — subtitles stay unattributed.")
                }
            }
        }

        // ── audio-only attribution: tag each line with the most-overlapping turn ──
        if !turns.isEmpty {
            attributeByAudioOverlap(&transcript, turns: turns)
            let n = transcript.segments.filter { !$0.speakerKey.isEmpty }.count
            log("attribute: \(n)/\(transcript.segments.count) lines tagged from audio diarization")
        }

        // ── emit ────────────────────────────────────────────────────
        let srtURL = outDir.appendingPathComponent("\(stem).srt")
        try Emit.srt(transcript, to: srtURL)
        log("emit: \(srtURL.path)")
        log("analyze-audio: done → \(outDir.path)")
    }

    /// Assign each subtitle segment the speaker of the audio turn it overlaps most.
    /// This is the audio-only stand-in for the full `fuse` stage (which needs the
    /// video for the visual active-speaker signal).
    private func attributeByAudioOverlap(_ t: inout Transcript, turns: [AudioTurn]) {
        for i in t.segments.indices {
            let seg = t.segments[i]
            var bestSpeaker = ""
            var bestOverlap = 0
            for turn in turns {
                let lo = max(seg.startMs, turn.startMs)
                let hi = min(seg.endMs, turn.endMs)
                let overlap = hi - lo
                if overlap > bestOverlap {
                    bestOverlap = overlap
                    bestSpeaker = turn.speaker
                }
            }
            if bestOverlap > 0 {
                t.segments[i].speakerKey = bestSpeaker
                t.segments[i].speakerConf = 1
            }
        }
    }
}
