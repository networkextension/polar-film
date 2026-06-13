import Foundation
import AVFoundation
import Vision
import CoreGraphics

// P2a — attribute each subtitle line to the on-screen speaker by VISUAL active-
// speaker detection: within a line's time window, whoever's mouth moves the most
// is the speaker. Mouth motion = variance of inner-lip openness across sampled
// frames (Vision landmarks). The active face is then clustered (feature-print)
// into a stable "Speaker A/B/…" so the same person keeps the same label across
// lines. Fully offline. Audio diarization fusion + ArcFace identity come later.
enum Fuse {
    static let framesPerSegment = 6

    static func run(videoURL: URL, outDir: URL, transcript: Transcript) async throws -> Transcript {
        let speakersDir = outDir.appendingPathComponent("speakers", isDirectory: true)
        try? FileManager.default.createDirectory(at: speakersDir, withIntermediateDirectories: true)
        let asset = AVURLAsset(url: videoURL)
        let gen = AVAssetImageGenerator(asset: asset)
        gen.appliesPreferredTrackTransform = true
        gen.requestedTimeToleranceBefore = CMTime(seconds: 0.05, preferredTimescale: 600)
        gen.requestedTimeToleranceAfter = CMTime(seconds: 0.05, preferredTimescale: 600)
        gen.maximumSize = CGSize(width: 1280, height: 1280)

        var speakers: [SpeakerProto] = []  // stable Speaker A/B/… prototypes
        var out = transcript.segments

        for i in out.indices {
            let seg = out[i]
            let startS = Double(seg.startMs) / 1000.0
            let endS = max(startS + 0.1, Double(seg.endMs) / 1000.0)

            // Per face-position track: a list of (openness) across this window.
            // We key tracks by rounded center to associate the same face across frames.
            var tracks: [FaceTrack] = []
            let n = framesPerSegment
            for k in 0..<n {
                let t = startS + (endS - startS) * (Double(k) + 0.5) / Double(n)
                guard let cg = try? await gen.image(at: CMTime(seconds: t, preferredTimescale: 600)).image else { continue }
                for f in detectFacesWithMouth(cg) {
                    let open = f.openness
                    if let idx = tracks.firstIndex(where: { $0.matches(f.center) }) {
                        tracks[idx].add(open: open, obs: f)
                    } else {
                        tracks.append(FaceTrack(center: f.center, openings: [open], last: f))
                    }
                }
            }

            // Active speaker = track with the largest mouth-openness variation.
            guard let active = tracks.max(by: { $0.motion < $1.motion }), active.motion > 0.0 else {
                continue  // no on-screen speaker found (off-screen / no faces)
            }
            // Cluster the active face into a stable speaker id. Same speaker
            // requires BOTH a close face feature-print AND a close on-screen
            // position — because the generic feature-print can't reliably tell two
            // faces apart (it merges them), position disambiguates within a scene.
            // (Cross-cut re-identification wants ArcFace — see design doc.)
            var cluster = -1
            if let crop = active.last.crop, let fp = featurePrint(crop) {
                let before = speakers.count
                cluster = assignSpeaker(fp: fp, center: active.center, into: &speakers)
                // Save a representative face thumbnail the first time we see a
                // speaker → spk0.jpg/spk1.jpg/… (basis for naming in P3).
                if cluster == before {
                    try? Keyframes.writeJPEG(crop, to: speakersDir.appendingPathComponent("spk\(cluster).jpg"))
                }
            }
            out[i].speakerKey = cluster >= 0 ? "spk\(cluster)" : "spk?"
            out[i].speakerConf = min(1.0, active.motion * 8.0)  // rough confidence
            log(String(format: "  seg %d: active@x=%.2f motion=%.3f faces=%d → spk%d",
                       i, active.center.x, active.motion, tracks.count, cluster))
        }
        return Transcript(media: transcript.media, language: transcript.language, segments: out)
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
                    openness = (hi - lo)  // in face-box-normalized units (landmarks are relative to bbox)
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
