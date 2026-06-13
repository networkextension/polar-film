import Foundation
import AVFoundation
import CoreMedia

// Decode the video's audio track to 16 kHz mono Float32 samples via AVAssetReader.
// We feed these straight to WhisperKit's transcribe(audioArray:) — bypassing
// WhisperKit's own audio loader, whose resampling path mis-decodes on some hosts
// (e.g. macOS 14.0), producing silence-like garbage. AVAssetReader does the
// decode + sample-rate conversion + channel downmix itself. Also the input
// diarization will need (P2).
enum Demux {
    static func audioSamples16k(videoURL: URL) async throws -> [Float] {
        let asset = AVURLAsset(url: videoURL)
        let tracks = try await asset.loadTracks(withMediaType: .audio)
        guard let track = tracks.first else {
            throw Err("no audio track in \(videoURL.lastPathComponent)")
        }

        let reader = try AVAssetReader(asset: asset)
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatLinearPCM,
            AVSampleRateKey: 16000,
            AVNumberOfChannelsKey: 1,
            AVLinearPCMBitDepthKey: 32,
            AVLinearPCMIsFloatKey: true,
            AVLinearPCMIsBigEndianKey: false,
            AVLinearPCMIsNonInterleaved: false,
        ]
        let output = AVAssetReaderTrackOutput(track: track, outputSettings: settings)
        output.alwaysCopiesSampleData = false
        guard reader.canAdd(output) else { throw Err("cannot add audio reader output") }
        reader.add(output)
        guard reader.startReading() else {
            throw Err("audio reader failed to start: \(reader.error?.localizedDescription ?? "unknown")")
        }

        var samples: [Float] = []
        while reader.status == .reading, let sbuf = output.copyNextSampleBuffer() {
            if let block = CMSampleBufferGetDataBuffer(sbuf) {
                let len = CMBlockBufferGetDataLength(block)
                if len > 0 {
                    var bytes = [UInt8](repeating: 0, count: len)
                    let status = bytes.withUnsafeMutableBytes { ptr in
                        CMBlockBufferCopyDataBytes(block, atOffset: 0, dataLength: len, destination: ptr.baseAddress!)
                    }
                    if status == kCMBlockBufferNoErr {
                        bytes.withUnsafeBytes { (raw: UnsafeRawBufferPointer) in
                            samples.append(contentsOf: raw.bindMemory(to: Float.self))
                        }
                    }
                }
            }
            CMSampleBufferInvalidate(sbuf)
        }
        if reader.status == .failed {
            throw Err("audio read failed: \(reader.error?.localizedDescription ?? "unknown")")
        }
        return samples
    }
}
