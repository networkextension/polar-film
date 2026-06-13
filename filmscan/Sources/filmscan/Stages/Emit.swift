import Foundation

// Subtitle output. P0 emits plain SRT; once P2 attributes speakers, the line is
// prefixed with the character name (e.g. "Walter: ...").
enum Emit {
    static func srt(_ t: Transcript, to url: URL) throws {
        var blocks: [String] = []
        for (i, s) in t.segments.enumerated() {
            let text = s.personID.isEmpty ? s.text : "[\(s.speakerKey)] \(s.text)"
            blocks.append("""
            \(i + 1)
            \(timecode(s.startMs)) --> \(timecode(s.endMs))
            \(text)
            """)
        }
        try (blocks.joined(separator: "\n\n") + "\n").write(to: url, atomically: true, encoding: .utf8)
    }

    /// SRT timecode `HH:MM:SS,mmm`.
    static func timecode(_ ms: Int) -> String {
        let h = ms / 3_600_000
        let m = (ms % 3_600_000) / 60_000
        let s = (ms % 60_000) / 1000
        let milli = ms % 1000
        return String(format: "%02d:%02d:%02d,%03d", h, m, s, milli)
    }
}
