import ArgumentParser
import Foundation

struct Analyze: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Analyze a video → speaker-attributed subtitles + keyframes."
    )

    @Argument(help: "Path to the video file (mp4/mov; mkv needs demux — P1). Omit when using --from-manifest.")
    var video: String?

    @Option(name: .long, help: "Run the ANE analysis tier from an extract-manifest.json (pulls audio from the music library; no source video needed).")
    var fromManifest: String?

    @Option(name: .long, help: "Dock base URL (or env FILMSCAN_SERVER) — required with --from-manifest.")
    var server: String?

    @Option(name: .long, help: "Bearer access token (or env FILMSCAN_TOKEN) — required with --from-manifest.")
    var token: String?

    @Option(name: .long, help: "Workspace id (X-Workspace-Id) — for --from-manifest audio fetch.")
    var workspaceID: String?

    @Option(name: .long, help: "Film movie id — with --from-manifest, push the resulting SRT to that movie.")
    var mediaID: String?

    @Option(name: .shortAndLong, help: "Spoken language code.")
    var lang: String = "en"

    @Option(name: .long, help: "Whisper model (base.en, small.en, …).")
    var model: String = "base.en"

    @Option(name: .long, help: "Path to a pre-downloaded WhisperKit model folder (skips the HF download).")
    var modelFolder: String?

    @Option(name: .long, help: "HuggingFace hub base (…/huggingface) with the pre-downloaded tokenizer; loaded locally so WhisperKit never reaches HF (needed under launchd / offline).")
    var tokenizerFolder: String?

    @Option(name: .long, help: "Keyframe sampling interval in seconds.")
    var frameInterval: Double = 2.0

    @Option(name: .long, help: "CoreML compute units: default | cpu | cpuAndGPU | cpuAndNeuralEngine | all. Use 'cpu' if transcription comes out garbled (macOS 14.0 ANE bug).")
    var compute: String = "default"

    @Flag(name: .long, inversion: .prefixedNo, help: "Audio speaker diarization to attribute off-screen/voiceover lines. On by default; --no-diarize for visual-only.")
    var diarize: Bool = true

    @Option(name: .long, help: "Folder with FluidAudio CoreML models (pyannote_segmentation.mlmodelc + wespeaker_v2.mlmodelc); omit to auto-download.")
    var diarModels: String?

    @Option(name: .shortAndLong, help: "Output directory (default: alongside the video / manifest).")
    var out: String?

    func run() async throws {
        if let manifest = fromManifest {
            try await runFromManifest(manifestPath: manifest)
            return
        }
        guard let video else {
            throw ValidationError("provide a video path, or --from-manifest <extract-manifest.json>")
        }
        let videoURL = URL(fileURLWithPath: video)
        guard FileManager.default.fileExists(atPath: videoURL.path) else {
            throw ValidationError("video not found: \(video)")
        }
        let mediaName = videoURL.deletingPathExtension().lastPathComponent
        let base = out.map { URL(fileURLWithPath: $0) } ?? videoURL.deletingLastPathComponent()
        let outDir = base.appendingPathComponent(mediaName + ".filmscan", isDirectory: true)
        try FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)

        let pipeline = Pipeline(videoURL: videoURL, outDir: outDir, lang: lang, model: model,
                                modelFolder: modelFolder, tokenizerFolder: tokenizerFolder,
                                frameIntervalSec: frameInterval, compute: compute,
                                diarize: diarize, diarModels: diarModels)
        try await pipeline.run()
    }

    // ── manifest tier: ANE analysis on a different box than extract ──
    // The extract stage already uploaded the audio to the music library and the
    // keyframes/faces to the photo library. Here we pull the audio back by its
    // track id and run the ANE-bound work — transcription + diarization — then
    // attribute each line from the diarization turns (audio-only; visual fusion
    // needs the source video, which this box doesn't have) and emit the SRT.
    private func runFromManifest(manifestPath: String) async throws {
        let base = (server ?? ProcessInfo.processInfo.environment["FILMSCAN_SERVER"] ?? "")
            .trimmingCharacters(in: .whitespaces)
        guard let baseURL = URL(string: base), base.hasPrefix("http") else {
            throw ValidationError("--server (or FILMSCAN_SERVER) is required with --from-manifest")
        }
        let tok = (token ?? ProcessInfo.processInfo.environment["FILMSCAN_TOKEN"] ?? "")
            .trimmingCharacters(in: .whitespaces)
        guard !tok.isEmpty else { throw ValidationError("--token (or FILMSCAN_TOKEN) is required with --from-manifest") }

        let manifestURL = URL(fileURLWithPath: manifestPath)
        let manifest = try loadJSON(ExtractManifest.self, from: manifestURL)
        guard let trackID = manifest.audioTrackID else {
            throw ValidationError("manifest has no audioTrackID — nothing to analyze")
        }
        let ws = workspaceID ?? manifest.workspaceID
        let outDir = out.map { URL(fileURLWithPath: $0) } ?? manifestURL.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)

        // 1. pull audio from the music library
        let audioURL = outDir.appendingPathComponent("audio.m4a")
        if !FileManager.default.fileExists(atPath: audioURL.path) {
            log("analyze: fetching audio track \(trackID) from music library …")
            let music = MusicClient(base: baseURL, token: tok, workspaceID: ws)
            try await music.downloadAudio(trackID: trackID, to: audioURL)
        }
        log("analyze: decoding audio → 16kHz mono …")
        let samples = try await Demux.audioSamples16k(videoURL: audioURL)

        // 2. transcribe (ANE-preferred; CPU fallback)
        log("analyze: WhisperKit \(model) …")
        var transcript = try await Transcribe.run(videoURL: audioURL, samples: samples,
                                                  lang: lang, model: model,
                                                  modelFolder: modelFolder, tokenizerFolder: tokenizerFolder,
                                                  compute: compute)
        try saveJSON(transcript, to: outDir.appendingPathComponent("transcript.json"))
        log("analyze: \(transcript.segments.count) segments")

        // 3. diarize + audio-only attribution (visual fusion needs the video)
        if diarize {
            do {
                log("analyze: FluidAudio diarization …")
                let turns = try await Diarize.run(samples: samples, modelsDir: diarModels, compute: compute)
                try saveJSON(Diarization(turns: turns), to: outDir.appendingPathComponent("diarize.json"))
                for i in transcript.segments.indices {
                    let (spk, frac) = Fuse.dominantAudioSpeaker(turns,
                        startMs: transcript.segments[i].startMs, endMs: transcript.segments[i].endMs)
                    if let spk { transcript.segments[i].speakerKey = spk; transcript.segments[i].speakerConf = frac }
                }
                let n = Set(turns.map { $0.speaker }).count
                log("analyze: \(turns.count) turns / \(n) speakers (audio-only attribution; no visual fusion without source video)")
            } catch {
                log("analyze: diarize FAILED (\(error.localizedDescription)) — emitting unattributed subtitles")
            }
        }

        // 4. emit SRT
        let stem = manifestURL.deletingPathExtension().lastPathComponent
            .replacingOccurrences(of: "extract-manifest", with: "subtitles")
        let srtURL = outDir.appendingPathComponent("\(stem).srt")
        try Emit.srt(transcript, to: srtURL)
        log("analyze: emitted \(srtURL.path)")

        // 5. push the SRT to the film movie (keyframes/faces were already
        // uploaded by the extract stage). Reuses Push.swift's FilmClient.
        if let mediaID, !mediaID.isEmpty {
            let client = FilmClient(base: baseURL, token: tok, workspaceID: ws, mediaID: mediaID)
            let content = try String(contentsOf: srtURL, encoding: .utf8)
            let n = try await client.uploadSubtitle(lang: lang, content: content)
            log("analyze: pushed subtitles to movie \(mediaID) — \(n) segments")
        }
        log("analyze: done")
    }
}
