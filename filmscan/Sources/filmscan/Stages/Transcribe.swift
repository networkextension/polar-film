import Foundation
import WhisperKit
import CoreML

// ASR via WhisperKit (CoreML Whisper, on-device, word-level timestamps).
// Loads + resamples the video's audio track itself (AVFoundation), so for P0 we
// feed the video path directly. NOTE: AVFoundation can't read Matroska (.mkv) —
// those need an explicit demux first (P1).
enum Transcribe {
    static func run(videoURL: URL, samples: [Float], lang: String, model: String, modelFolder: String? = nil, compute: String = "default") async throws -> Transcript {
        var config = WhisperKitConfig(model: model, modelFolder: modelFolder)
        if let units = computeUnits(compute) {
            // Force the compute path — Neural Engine (the default for the text
            // decoder) produces garbage tokens on some hosts (macOS 14.0); CPU is
            // deterministic. See --compute.
            config = WhisperKitConfig(model: model, modelFolder: modelFolder,
                computeOptions: ModelComputeOptions(melCompute: units, audioEncoderCompute: units,
                                                    textDecoderCompute: units, prefillCompute: units))
        }
        let whisper = try await WhisperKit(config)
        let options = DecodingOptions(
            language: lang,
            wordTimestamps: true
        )
        // Feed pre-decoded 16 kHz mono samples (from Demux) — bypasses WhisperKit's
        // own audio loader, which mis-decodes on some hosts (macOS 14.0).
        let results = try await whisper.transcribe(audioArray: samples, decodeOptions: options)

        var segments: [Segment] = []
        var idx = 0
        for result in results {
            for s in result.segments {
                let words: [Word] = (s.words ?? []).map { w in
                    Word(text: w.word.trimmingCharacters(in: .whitespaces),
                         startMs: Int((w.start * 1000).rounded()),
                         endMs: Int((w.end * 1000).rounded()))
                }
                segments.append(Segment(
                    idx: idx,
                    startMs: Int((s.start * 1000).rounded()),
                    endMs: Int((s.end * 1000).rounded()),
                    text: cleanText(s.text),
                    words: words
                ))
                idx += 1
            }
        }
        return Transcript(media: MediaInfo(path: videoURL.path), language: lang, segments: segments)
    }

    /// Map a `--compute` flag to MLComputeUnits; nil = WhisperKit defaults.
    static func computeUnits(_ s: String) -> MLComputeUnits? {
        switch s.lowercased() {
        case "cpu": return .cpuOnly
        case "gpu", "cpuandgpu": return .cpuAndGPU
        case "ane", "cpuandneuralengine": return .cpuAndNeuralEngine
        case "all": return .all
        default: return nil
        }
    }

    /// Strip Whisper special tokens (`<|startoftranscript|>`, `<|5.68|>`, …) and
    /// collapse whitespace, leaving plain subtitle text.
    static func cleanText(_ s: String) -> String {
        let stripped = s.replacingOccurrences(of: "<\\|[^|>]*\\|>", with: "", options: .regularExpression)
        return stripped
            .replacingOccurrences(of: "\\s+", with: " ", options: .regularExpression)
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
