import Foundation
import AVFoundation
import Vision
import CoreGraphics

// Attribute each subtitle line to a speaker. Two signals:
//   • visual active-speaker (P2a): within a line's window, whoever's mouth moves
//     the most (inner-lip openness variance via Vision landmarks).
//   • audio diarization (P5): "who spoke when" from voice embeddings (FluidAudio).
//
// Fusion (when audio turns are present) makes audio the identity BACKBONE — a
// global cluster that survives scene cuts and covers off-screen / voiceover lines
// the camera never shows — and uses the visual active face to pick each speaker's
// representative thumbnail. Lines outside any audio turn fall back to the visual
// clustering. With no audio turns at all, it runs the pure-visual algorithm
// unchanged. Fully offline.
enum Fuse {
    static let framesPerSegment = 6

    static func run(videoURL: URL, outDir: URL, transcript: Transcript, audioTurns: [AudioTurn]) async throws -> Transcript {
        if audioTurns.isEmpty {
            return try await runVisual(videoURL: videoURL, outDir: outDir, transcript: transcript)
        }
        return try await runFused(videoURL: videoURL, outDir: outDir, transcript: transcript, audioTurns: audioTurns)
    }

    // MARK: - fused (audio backbone + visual naming)

    private static func runFused(videoURL: URL, outDir: URL, transcript: Transcript, audioTurns: [AudioTurn]) async throws -> Transcript {
        let speakersDir = try makeSpeakersDir(outDir)
        let gen = imageGenerator(videoURL)
        var out = transcript.segments

        // Pass 1 — per line: the visual active face (if any) + the dominant audio speaker.
        var faces: [MouthFace?] = Array(repeating: nil, count: out.count)
        var motions = [Double](repeating: 0, count: out.count)
        var audios: [String?] = Array(repeating: nil, count: out.count)
        var overlaps = [Double](repeating: 0, count: out.count)
        for i in out.indices {
            let (startS, endS) = window(out[i])
            if let active = await activeFace(gen: gen, startS: startS, endS: endS) {
                faces[i] = active.face; motions[i] = active.motion
            }
            let (spk, frac) = dominantAudioSpeaker(audioTurns, startMs: out[i].startMs, endMs: out[i].endMs)
            audios[i] = spk; overlaps[i] = frac
        }

        // Pass 2a — audio-backed lines: stable spk index by first appearance; keep the
        // highest-motion visual face per audio speaker as its representative thumbnail.
        var audioIdx: [String: Int] = [:]
        var audioRep: [Int: (motion: Double, crop: CGImage?)] = [:]
        var offScreen = 0
        for i in out.indices {
            guard let a = audios[i] else { continue }
            let idx = audioIdx[a] ?? { let v = audioIdx.count; audioIdx[a] = v; return v }()
            if let f = faces[i], motions[i] > (audioRep[idx]?.motion ?? -1) {
                audioRep[idx] = (motions[i], f.crop)
            }
            if faces[i] == nil { offScreen += 1 }
            out[i].speakerKey = "spk\(idx)"
            // Confidence: audio overlap fraction, boosted when a visual face agrees.
            out[i].speakerConf = min(1.0, overlaps[i] * (faces[i] != nil ? 1.2 : 1.0))
            log(String(format: "  seg %d: audio=%@ overlap=%.2f face=%@ → spk%d",
                       i, a, overlaps[i], faces[i] != nil ? "yes" : "no", idx))
        }
        let audioCount = audioIdx.count

        // Pass 2b — lines outside any audio turn: fall back to visual clustering,
        // numbered after the audio speakers.
        var visualProtos: [SpeakerProto] = []
        for i in out.indices where audios[i] == nil {
            guard let f = faces[i], let crop = f.crop, let fp = featurePrint(crop) else {
                out[i].speakerKey = "spk?"
                continue
            }
            let before = visualProtos.count
            let c = assignSpeaker(fp: fp, center: f.center, into: &visualProtos)
            let idx = audioCount + c
            if c == before { try? Keyframes.writeJPEG(crop, to: speakersDir.appendingPathComponent("spk\(idx).jpg")) }
            out[i].speakerKey = "spk\(idx)"
            out[i].speakerConf = min(1.0, motions[i] * 8.0)
            log(String(format: "  seg %d: no-audio visual → spk%d", i, idx))
        }

        // Export a representative thumbnail per audio speaker that was ever on screen.
        for (idx, rep) in audioRep {
            if let crop = rep.crop { try? Keyframes.writeJPEG(crop, to: speakersDir.appendingPathComponent("spk\(idx).jpg")) }
        }
        log("fuse: \(audioCount) audio speaker(s), \(offScreen) off-screen line(s) recovered")
        return Transcript(media: transcript.media, language: transcript.language, segments: out)
    }

    // MARK: - visual only (P2a/P2b — fallback when no audio turns)

    private static func runVisual(videoURL: URL, outDir: URL, transcript: Transcript) async throws -> Transcript {
        let speakersDir = try makeSpeakersDir(outDir)
        let gen = imageGenerator(videoURL)
        var speakers: [SpeakerProto] = []
        var out = transcript.segments
        for i in out.indices {
            let (startS, endS) = window(out[i])
            guard let active = await activeFace(gen: gen, startS: startS, endS: endS) else {
                continue  // no on-screen speaker found (off-screen / no faces)
            }
            var cluster = -1
            if let crop = active.face.crop, let fp = featurePrint(crop) {
                let before = speakers.count
                cluster = assignSpeaker(fp: fp, center: active.face.center, into: &speakers)
                if cluster == before {
                    try? Keyframes.writeJPEG(crop, to: speakersDir.appendingPathComponent("spk\(cluster).jpg"))
                }
            }
            out[i].speakerKey = cluster >= 0 ? "spk\(cluster)" : "spk?"
            out[i].speakerConf = min(1.0, active.motion * 8.0)
            log(String(format: "  seg %d: active@x=%.2f motion=%.3f → spk%d",
                       i, active.face.center.x, active.motion, cluster))
        }
        return Transcript(media: transcript.media, language: transcript.language, segments: out)
    }

    // MARK: - shared helpers

    private static func makeSpeakersDir(_ outDir: URL) throws -> URL {
        let dir = outDir.appendingPathComponent("speakers", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    private static func imageGenerator(_ videoURL: URL) -> AVAssetImageGenerator {
        let gen = AVAssetImageGenerator(asset: AVURLAsset(url: videoURL))
        gen.appliesPreferredTrackTransform = true
        gen.requestedTimeToleranceBefore = CMTime(seconds: 0.05, preferredTimescale: 600)
        gen.requestedTimeToleranceAfter = CMTime(seconds: 0.05, preferredTimescale: 600)
        gen.maximumSize = CGSize(width: 1280, height: 1280)
        return gen
    }

    private static func window(_ seg: Segment) -> (Double, Double) {
        let startS = Double(seg.startMs) / 1000.0
        return (startS, max(startS + 0.1, Double(seg.endMs) / 1000.0))
    }

    /// The most-talking on-screen face across a line's window (max mouth-openness
    /// spread), or nil if no face is found. Faces are associated across the sampled
    /// frames by on-screen position (a face-position track).
    private static func activeFace(gen: AVAssetImageGenerator, startS: Double, endS: Double) async -> (face: MouthFace, motion: Double)? {
        var tracks: [FaceTrack] = []
        let n = framesPerSegment
        for k in 0..<n {
            let t = startS + (endS - startS) * (Double(k) + 0.5) / Double(n)
            guard let cg = try? await gen.image(at: CMTime(seconds: t, preferredTimescale: 600)).image else { continue }
            for f in detectFacesWithMouth(cg) {
                if let idx = tracks.firstIndex(where: { $0.matches(f.center) }) {
                    tracks[idx].add(open: f.openness, obs: f)
                } else {
                    tracks.append(FaceTrack(center: f.center, openings: [f.openness], last: f))
                }
            }
        }
        guard let active = tracks.max(by: { $0.motion < $1.motion }), active.motion > 0.0 else { return nil }
        return (active.last, active.motion)
    }

    /// The audio speaker covering the most of [startMs,endMs], and the fraction of
    /// the line that speaker covers (0..1). nil when no turn overlaps the line.
    static func dominantAudioSpeaker(_ turns: [AudioTurn], startMs: Int, endMs: Int) -> (String?, Double) {
        let dur = Double(max(1, endMs - startMs))
        var bySpeaker: [String: Int] = [:]
        for t in turns {
            let ov = min(endMs, t.endMs) - max(startMs, t.startMs)
            if ov > 0 { bySpeaker[t.speaker, default: 0] += ov }
        }
        guard let best = bySpeaker.max(by: { $0.value < $1.value }) else { return (nil, 0) }
        return (best.key, min(1.0, Double(best.value) / dur))
    }

    // MARK: face + mouth

    struct MouthFace {
        let center: CGPoint      // normalized face center
        let openness: Double     // inner-lip vertical extent / face height
        let crop: CGImage?
    }

    static func detectFacesWithMouth(_ cg: CGImage) -> [MouthFace] {
        let req = VNDetectFaceLandmarksRequest()
        try? VNImageRequestHandler(cgImage: cg, options: [:]).perform([req])
        var result: [MouthFace] = []
        for obs in req.results ?? [] {
            let bb = obs.boundingBox
            let center = CGPoint(x: bb.midX, y: bb.midY)
            var openness = 0.0
            if let lips = obs.landmarks?.innerLips {
                let ys = lips.normalizedPoints.map { Double($0.y) }
                if let lo = ys.min(), let hi = ys.max() {
                    openness = (hi - lo)  // face-box-normalized (landmarks are relative to bbox)
                }
            }
            result.append(MouthFace(center: center, openness: openness,
                                    crop: FacesStage.cropFace(cg, bbox: bb)))
        }
        return result
    }

    struct SpeakerProto { var fp: VNFeaturePrintObservation; var center: CGPoint }

    /// Match to an existing speaker only when face descriptor AND position both
    /// agree; otherwise start a new speaker. Updates the matched speaker's
    /// position so it tracks slow drift within a scene.
    static func assignSpeaker(fp: VNFeaturePrintObservation, center: CGPoint, into speakers: inout [SpeakerProto]) -> Int {
        var best = -1
        var bestDist = Float.greatestFiniteMagnitude
        for (i, sp) in speakers.enumerated() {
            let posClose = hypot(sp.center.x - center.x, sp.center.y - center.y) < 0.15
            guard posClose else { continue }
            var d: Float = 0
            try? sp.fp.computeDistance(&d, to: fp)
            if d < bestDist { bestDist = d; best = i }
        }
        if best >= 0, bestDist < 24.0 { speakers[best].center = center; return best }
        speakers.append(SpeakerProto(fp: fp, center: center))
        return speakers.count - 1
    }

    static func featurePrint(_ cg: CGImage) -> VNFeaturePrintObservation? {
        let req = VNGenerateImageFeaturePrintRequest()
        try? VNImageRequestHandler(cgImage: cg, options: [:]).perform([req])
        return req.results?.first
    }
}

/// Accumulates one on-screen face's mouth openings across a segment's frames.
private struct FaceTrack {
    var center: CGPoint
    var openings: [Double]
    var last: Fuse.MouthFace

    mutating func add(open: Double, obs: Fuse.MouthFace) { openings.append(open); last = obs; center = obs.center }
    func matches(_ c: CGPoint) -> Bool { hypot(center.x - c.x, center.y - c.y) < 0.12 }
    /// Talking signal: spread of mouth openness (a talking face opens/closes; a
    /// listening face stays roughly constant).
    var motion: Double {
        guard openings.count >= 2, let lo = openings.min(), let hi = openings.max() else { return 0 }
        return hi - lo
    }
}
