import ArgumentParser
import Foundation

// P3 — name the anonymous speaker clusters the fuse stage produced (spk0, spk1, …)
// so the subtitles read "[Darcy] ..." instead of "[spk0] ...". Naming is a tiny
// persisted map (<out>/names.json: spk0 → "Darcy"); applying it rewrites each
// segment's personID and re-emits the SRT. The spk*.jpg thumbnails under
// <out>/speakers/ are the visual reference for who each cluster is. TMDB cast
// auto-match is a later add-on; this is the manual ground truth it would seed.
struct Label: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Name speaker clusters (spk0 → \"Darcy\") and re-emit the subtitles."
    )

    @Argument(help: "The .filmscan output directory produced by `analyze`.")
    var out: String

    @Option(name: .long, help: "Assign a name, e.g. --set spk0=Darcy (repeatable).")
    var set: [String] = []

    @Flag(name: .long, help: "List the speaker clusters, line counts, names, and thumbnails.")
    var list: Bool = false

    func run() async throws {
        let outDir = URL(fileURLWithPath: out)
        let fusedURL = outDir.appendingPathComponent("fused.json")
        guard let transcript = try? loadJSON(Transcript.self, from: fusedURL) else {
            throw ValidationError("no fused.json in \(out) — run `analyze` first (and it must have attributed speakers).")
        }
        let namesURL = outDir.appendingPathComponent("names.json")
        var names = (try? loadJSON([String: String].self, from: namesURL)) ?? [:]

        // Apply any --set assignments, then persist.
        for entry in set {
            let parts = entry.split(separator: "=", maxSplits: 1).map(String.init)
            guard parts.count == 2, !parts[0].isEmpty else {
                throw ValidationError("bad --set '\(entry)' — expected spkN=Name")
            }
            names[parts[0]] = parts[1].trimmingCharacters(in: .whitespaces)
        }
        if !set.isEmpty {
            try saveJSON(names, to: namesURL)
        }

        // No assignments → just show the roster and exit (nothing to re-emit).
        if set.isEmpty {
            printRoster(transcript, names: names, outDir: outDir)
            return
        }
        if list { printRoster(transcript, names: names, outDir: outDir) }

        // Re-emit: stamp personID from the names map, rewrite fused.json + SRT.
        var t = transcript
        for i in t.segments.indices {
            t.segments[i].personID = names[t.segments[i].speakerKey] ?? ""
        }
        try saveJSON(t, to: fusedURL)
        let stem = URL(fileURLWithPath: t.media.path).deletingPathExtension().lastPathComponent
        let srtURL = outDir.appendingPathComponent("\(stem).srt")
        try Emit.srt(t, to: srtURL)
        let named = t.segments.filter { !$0.personID.isEmpty }.count
        log("label: \(named)/\(t.segments.count) lines named → \(srtURL.path)")
    }

    /// Print one row per speaker cluster: line count, current name, thumbnail.
    private func printRoster(_ t: Transcript, names: [String: String], outDir: URL) {
        var counts: [String: Int] = [:]
        for s in t.segments where !s.speakerKey.isEmpty { counts[s.speakerKey, default: 0] += 1 }
        let fm = FileManager.default
        print("speaker  lines  name              thumbnail")
        for key in counts.keys.sorted() {
            let n = counts[key] ?? 0
            let name = names[key] ?? "—"
            let thumbURL = outDir.appendingPathComponent("speakers/\(key).jpg")
            let thumb = fm.fileExists(atPath: thumbURL.path) ? thumbURL.path : "—"
            let row = key.padding(toLength: 7, withPad: " ", startingAt: 0)
                + String(format: " %5d  ", n)
                + name.padding(toLength: 16, withPad: " ", startingAt: 0)
                + "  " + thumb
            print(row)
        }
    }
}
