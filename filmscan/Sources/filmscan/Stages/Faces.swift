import Foundation
import Vision
import CoreGraphics
import ImageIO

// Detect faces on each keyframe and cluster them into characters. Uses Vision
// face detection + an image feature-print of the face crop as a cheap embedding;
// online greedy clustering by feature-print distance. (P3 can swap in ArcFace
// for cast-photo matching.) Output drives the fuse stage's face↔speaker mapping.
enum FacesStage {
    static func run(outDir: URL, frames: Frames, threshold: Float = 18.0) throws -> Faces {
        var dets: [FaceDet] = []
        var prototypes: [VNFeaturePrintObservation] = []  // one prototype per cluster
        var loggedDim = false

        for f in frames.frames {
            guard let cg = loadCGImage(outDir.appendingPathComponent(f.file)) else { continue }
            for obs in detectFaces(cg) {
                let bb = obs.boundingBox  // normalized, origin BOTTOM-left
                let box = Box(x: Double(bb.minX), y: Double(1 - bb.maxY),
                              w: Double(bb.width), h: Double(bb.height))
                var cluster = -1
                var emb: [Float]? = nil
                if let crop = cropFace(cg, bbox: bb), let fp = featurePrint(crop) {
                    cluster = assign(fp, &prototypes, threshold)
                    // PF-14: keep the feature-print vector (used for clustering)
                    // so the server can do re-identification (pgvector).
                    let v = fpToFloats(fp)
                    if !v.isEmpty {
                        emb = v
                        if !loggedDim {
                            FileHandle.standardError.write("filmscan: face feature-print dim = \(v.count)\n".data(using: .utf8)!)
                            loggedDim = true
                        }
                    }
                }
                dets.append(FaceDet(frameIdx: f.idx, timeMs: f.timeMs, box: box, cluster: cluster, embedding: emb))
            }
        }
        return Faces(faces: dets, clusterCount: prototypes.count)
    }

    static func detectFaces(_ cg: CGImage) -> [VNFaceObservation] {
        let req = VNDetectFaceRectanglesRequest()
        try? VNImageRequestHandler(cgImage: cg, options: [:]).perform([req])
        return req.results ?? []
    }

    static func featurePrint(_ cg: CGImage) -> VNFeaturePrintObservation? {
        let req = VNGenerateImageFeaturePrintRequest()
        // Pin revision 2 (768-d) for a stable embedding dimension across machines.
        req.revision = VNGenerateImageFeaturePrintRequestRevision2
        try? VNImageRequestHandler(cgImage: cg, options: [:]).perform([req])
        return req.results?.first
    }

    /// Convert a feature-print observation to a plain Float vector (handles
    /// both float and double element types).
    static func fpToFloats(_ fp: VNFeaturePrintObservation) -> [Float] {
        let n = fp.elementCount
        guard n > 0 else { return [] }
        switch fp.elementType {
        case .float:
            return fp.data.withUnsafeBytes { Array($0.bindMemory(to: Float.self).prefix(n)) }
        case .double:
            return fp.data.withUnsafeBytes { $0.bindMemory(to: Double.self).prefix(n).map { Float($0) } }
        default:
            return []
        }
    }

    /// Greedy online clustering: nearest prototype within `threshold`, else new cluster.
    static func assign(_ fp: VNFeaturePrintObservation, _ protos: inout [VNFeaturePrintObservation], _ threshold: Float) -> Int {
        var best = -1
        var bestDist = Float.greatestFiniteMagnitude
        for (i, p) in protos.enumerated() {
            var d: Float = 0
            try? p.computeDistance(&d, to: fp)
            if d < bestDist { bestDist = d; best = i }
        }
        if best >= 0, bestDist < threshold { return best }
        protos.append(fp)
        return protos.count - 1
    }

    /// Crop the (slightly padded) face region. Vision bbox is normalized,
    /// origin bottom-left; CGImage.cropping uses pixel coords, origin top-left.
    static func cropFace(_ cg: CGImage, bbox: CGRect) -> CGImage? {
        let w = CGFloat(cg.width), h = CGFloat(cg.height)
        let pad: CGFloat = 0.1
        let x = max(0, bbox.minX - bbox.width * pad) * w
        let yTop = max(0, (1 - bbox.maxY) - bbox.height * pad) * h
        let cw = min(w - x, bbox.width * (1 + 2 * pad) * w)
        let ch = min(h - yTop, bbox.height * (1 + 2 * pad) * h)
        guard cw > 1, ch > 1 else { return nil }
        return cg.cropping(to: CGRect(x: x, y: yTop, width: cw, height: ch))
    }

    static func loadCGImage(_ url: URL) -> CGImage? {
        guard let src = CGImageSourceCreateWithURL(url as CFURL, nil) else { return nil }
        return CGImageSourceCreateImageAtIndex(src, 0, nil)
    }
}
