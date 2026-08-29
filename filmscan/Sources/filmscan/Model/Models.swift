import Foundation

// Intermediate artifacts each stage reads/writes under <out>/<media>/.
// Codable so the pipeline is resumable: a stage is skipped when its JSON exists.

/// A word with millisecond timing (from WhisperKit word timestamps). Used later
/// to align subtitle text with speaker turns.
struct Word: Codable, Hashable {
    var text: String
    var startMs: Int
    var endMs: Int
}

/// One subtitle line: a span of speech with text and (optionally) its words.
/// `speakerKey` / `personID` / `speakerConf` stay empty until the diarize+fuse
/// stages attribute the line to a character (P2).
struct Segment: Codable, Hashable {
    var idx: Int
    var startMs: Int
    var endMs: Int
    var text: String
    var words: [Word] = []

    // filled by P2 (fuse):
    var speakerKey: String = ""   // anonymous cluster id, e.g. "spk0" / face cluster
    var personID: String = ""     // resolved person/character id once labeled
    var speakerConf: Double = 0   // 0..1
}

/// Probe metadata about the source video.
struct MediaInfo: Codable {
    var path: String
    var durationMs: Int = 0
    var fps: Double = 0
    var width: Int = 0
    var height: Int = 0
}

/// The transcript artifact (output of the Transcribe stage).
struct Transcript: Codable {
    var media: MediaInfo
    var language: String
    var segments: [Segment]
}

/// One sampled keyframe (output of the Keyframes stage).
struct Frame: Codable, Hashable {
    var idx: Int
    var timeMs: Int
    var file: String   // relative path under <out>/frames/
}

struct Frames: Codable {
    var frames: [Frame]
}

/// A normalized face box in [0,1], origin top-left.
struct Box: Codable, Hashable {
    var x: Double, y: Double, w: Double, h: Double
}

/// One detected face on a keyframe (output of the Faces stage). `cluster` groups
/// the same person across frames → a character (named later by `label`).
struct FaceDet: Codable, Hashable {
    var frameIdx: Int
    var timeMs: Int
    var box: Box
    var cluster: Int    // face-cluster id (-1 = unassigned)
}

struct Faces: Codable {
    var faces: [FaceDet]
    var clusterCount: Int
}

/// One audio speaker turn (output of the Diarize stage): "who spoke when",
/// from voice embeddings — independent of what's on screen. Fused with the
/// visual active-speaker signal to attribute off-screen / voiceover lines.
struct AudioTurn: Codable, Hashable {
    var speaker: String   // diarizer cluster id, e.g. "Speaker 1"
    var startMs: Int
    var endMs: Int
}

struct Diarization: Codable {
    var turns: [AudioTurn]
}
