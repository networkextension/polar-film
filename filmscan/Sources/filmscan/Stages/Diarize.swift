import Foundation
import CoreML
import FluidAudio

// Audio speaker diarization via FluidAudio (pre-converted CoreML pyannote
// segmentation + wespeaker embeddings). Takes the same 16 kHz mono Float samples
// the transcribe stage uses (from Demux) and returns "who spoke when" turns —
// independent of what's on screen. The fuse stage uses these to attribute
// off-screen / voiceover lines and to bridge the same voice across scene cuts.
// Best-effort: a failure here leaves visual-only fusion.
//
// NOTE: on macOS 14.0 the Neural Engine path SIGSEGVs inside CoreML for these
// models — pass `compute: "cpu"` (as for WhisperKit) to force the CPU path.
// A hard crash there can't be caught in Swift, so the caller forwards --compute.
enum Diarize {
    /// Run diarization on 16 kHz mono samples. `modelsDir`, when set, points at a
    /// folder holding `pyannote_segmentation.mlmodelc` + `wespeaker_v2.mlmodelc`
    /// (from the FluidInference/speaker-diarization-coreml repo) for offline hosts;
    /// otherwise the models are downloaded on first use. `compute` maps to CoreML
    /// MLComputeUnits (cpu | cpuAndGPU | cpuAndNeuralEngine | all); nil = library default.
    static func run(samples: [Float], modelsDir: String?, compute: String) async throws -> [AudioTurn] {
        let config: MLModelConfiguration?
        if let units = Transcribe.computeUnits(compute) {
            let c = MLModelConfiguration()
            c.computeUnits = units
            config = c
        } else {
            config = nil
        }

        let models: DiarizerModels
        if let dir = modelsDir, !dir.isEmpty {
            let base = URL(fileURLWithPath: dir)
            models = try DiarizerModels.load(
                localSegmentationModel: base.appendingPathComponent("pyannote_segmentation.mlmodelc"),
                localEmbeddingModel: base.appendingPathComponent("wespeaker_v2.mlmodelc"),
                configuration: config)
        } else {
            models = try await DiarizerModels.downloadIfNeeded(configuration: config)
        }
        let diarizer = DiarizerManager()
        diarizer.initialize(models: models)
        let result = try diarizer.performCompleteDiarization(samples)
        return result.segments.map {
            AudioTurn(speaker: $0.speakerId,
                      startMs: Int(($0.startTimeSeconds * 1000).rounded()),
                      endMs: Int(($0.endTimeSeconds * 1000).rounded()))
        }
    }
}
