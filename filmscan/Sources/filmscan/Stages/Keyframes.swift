import Foundation
import AVFoundation
import CoreGraphics
import ImageIO
import UniformTypeIdentifiers

// Sample keyframes from the video. P1 = uniform interval; P2 will switch to
// subtitle-midpoints + scene cuts. Frames are written as JPEG under <out>/frames/.
enum Keyframes {
    static func run(videoURL: URL, outDir: URL, everySec: Double) async throws -> Frames {
        let framesDir = outDir.appendingPathComponent("frames", isDirectory: true)
        try FileManager.default.createDirectory(at: framesDir, withIntermediateDirectories: true)

        let asset = AVURLAsset(url: videoURL)
        let duration = try await asset.load(.duration)
        let totalSec = CMTimeGetSeconds(duration)
        guard totalSec.isFinite, totalSec > 0 else {
            throw Err("could not read video duration (mp4/mov only; mkv needs demux)")
        }

        let gen = AVAssetImageGenerator(asset: asset)
        gen.appliesPreferredTrackTransform = true
        gen.requestedTimeToleranceBefore = CMTime(seconds: 0.2, preferredTimescale: 600)
        gen.requestedTimeToleranceAfter = CMTime(seconds: 0.2, preferredTimescale: 600)
        gen.maximumSize = CGSize(width: 1280, height: 1280)

        var out: [Frame] = []
        var idx = 0
        var t = 0.0
        while t < totalSec {
            let time = CMTime(seconds: t, preferredTimescale: 600)
            do {
                let (cg, _) = try await gen.image(at: time)
                let file = String(format: "frame_%05d.jpg", idx)
                try writeJPEG(cg, to: framesDir.appendingPathComponent(file))
                out.append(Frame(idx: idx, timeMs: Int((t * 1000).rounded()), file: "frames/\(file)"))
                idx += 1
            } catch {
                log("keyframes: skip t=\(t)s (\(error.localizedDescription))")
            }
            t += everySec
        }
        return Frames(frames: out)
    }

    static func writeJPEG(_ cg: CGImage, to url: URL) throws {
        guard let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.jpeg.identifier as CFString, 1, nil) else {
            throw Err("CGImageDestination create failed")
        }
        CGImageDestinationAddImage(dest, cg, [kCGImageDestinationLossyCompressionQuality: 0.8] as CFDictionary)
        guard CGImageDestinationFinalize(dest) else { throw Err("JPEG write failed: \(url.lastPathComponent)") }
    }
}

struct Err: LocalizedError { let msg: String; init(_ m: String) { msg = m }; var errorDescription: String? { msg } }
