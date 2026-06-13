import ArgumentParser
import Foundation
import Vision

// P3 — name the anonymous speaker clusters the fuse stage produced (spk0, spk1, …)
// so the subtitles read "[Darcy] ..." instead of "[spk0] ...". Naming is a tiny
// persisted map (<out>/names.json: spk0 → "Darcy"); applying it rewrites each
// segment's personID and re-emits the SRT. The spk*.jpg thumbnails under
// <out>/speakers/ are the visual reference for who each cluster is.
//
// Names can be set manually (--set spk0=Darcy) or auto-matched from a movie's
// TMDB cast (--tmdb-id / --tmdb-cast): each spkN.jpg is compared to the cast
// profile photos via Vision feature-print, and the nearest character within a
// distance threshold is assigned. The match is cross-domain (in-film frame vs
// promo headshot) so it's best-effort — low-confidence clusters are left for a
// manual --set, and ArcFace (P3b) would raise accuracy by swapping the embedding.
struct Label: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Name speaker clusters (manual or TMDB cast auto-match) and re-emit the subtitles."
    )

    @Argument(help: "The .filmscan output directory produced by `analyze`.")
    var out: String

    @Option(name: .long, help: "Assign a name, e.g. --set spk0=Darcy (repeatable). Wins over TMDB.")
    var set: [String] = []

    @Flag(name: .long, help: "List the speaker clusters, line counts, names, and thumbnails.")
    var list: Bool = false

    @Option(name: .long, help: "TMDB movie id — auto-name speakers from its cast.")
    var tmdbID: Int?

    @Option(name: .long, help: "TMDB API key (or env TMDB_API_KEY).")
    var tmdbKey: String?

    @Option(name: .long, help: "Pre-saved TMDB credits JSON (offline; profile_path may be a local file).")
    var tmdbCast: String?

    @Option(name: .long, help: "Max feature-print distance to accept an auto-match (lower = stricter).")
    var tmdbThreshold: Float = 22.0

    func run() async throws {
        let outDir = URL(fileURLWithPath: out)
        let fusedURL = outDir.appendingPathComponent("fused.json")
        guard let transcript = try? loadJSON(Transcript.self, from: fusedURL) else {
            throw ValidationError("no fused.json in \(out) — run `analyze` first (and it must have attributed speakers).")
        }
        let namesURL = outDir.appendingPathComponent("names.json")
        var names = (try? loadJSON([String: String].self, from: namesURL)) ?? [:]

        // Manual --set assignments (these win over TMDB).
        var manual = Set<String>()
        for entry in set {
            let parts = entry.split(separator: "=", maxSplits: 1).map(String.init)
            guard parts.count == 2, !parts[0].isEmpty else {
                throw ValidationError("bad --set '\(entry)' — expected spkN=Name")
            }
            names[parts[0]] = parts[1].trimmingCharacters(in: .whitespaces)
            manual.insert(parts[0])
        }

        // TMDB auto-match (doesn't clobber a manual --set for the same speaker).
        let tmdbRequested = tmdbID != nil || tmdbCast != nil
        if tmdbRequested {
            let added = try await tmdbMatch(transcript: transcript, outDir: outDir, skip: manual)
            for (spk, name) in added where names[spk] == nil { names[spk] = name }
        }

        // Nothing to change → just show the roster.
        if set.isEmpty && !tmdbRequested {
            printRoster(transcript, names: names, outDir: outDir)
            return
        }
        try saveJSON(names, to: namesURL)
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

    /// Match each spkN.jpg thumbnail to the nearest TMDB cast member's profile by
    /// Vision feature-print. Returns confident assignments (spk → character) and
    /// prints a table so the human sees what was auto-named vs left for manual.
    private func tmdbMatch(transcript: Transcript, outDir: URL, skip: Set<String>) async throws -> [String: String] {
        let cast: [TMDB.CastMember]
        if let file = tmdbCast {
            cast = try TMDB.loadCredits(file: URL(fileURLWithPath: file))
        } else {
            let key = (tmdbKey ?? ProcessInfo.processInfo.environment["TMDB_API_KEY"] ?? "").trimmingCharacters(in: .whitespaces)
            guard !key.isEmpty else { throw ValidationError("--tmdb-key (or env TMDB_API_KEY) required with --tmdb-id") }
            cast = try await TMDB.fetchCredits(id: tmdbID!, key: key)
        }
        guard !cast.isEmpty else { log("tmdb: empty cast"); return [:] }

        // Feature-print each cast member's profile (skip those without an image).
        var castFP: [(member: TMDB.CastMember, fp: VNFeaturePrintObservation)] = []
        for m in cast {
            guard let p = m.profilePath,
                  let cg = await TMDB.profileImage(p, baseDir: URL(fileURLWithPath: tmdbCast ?? ".").deletingLastPathComponent()),
                  let fp = Fuse.featurePrint(cg) else { continue }
            castFP.append((m, fp))
        }
        log("tmdb: \(cast.count) cast, \(castFP.count) with profile images")

        // Line counts per speaker (for the table) + the speaker keys to name.
        var counts: [String: Int] = [:]
        for s in transcript.segments where !s.speakerKey.isEmpty && s.speakerKey != "spk?" {
            counts[s.speakerKey, default: 0] += 1
        }

        var result: [String: String] = [:]
        print("speaker  lines  → character           dist   match")
        for spk in counts.keys.sorted() {
            let line = { (mark: String, ch: String, d: String) in
                print(spk.padding(toLength: 7, withPad: " ", startingAt: 0)
                    + String(format: " %5d  → ", counts[spk] ?? 0)
                    + ch.padding(toLength: 20, withPad: " ", startingAt: 0)
                    + " \(d)  \(mark)")
            }
            if skip.contains(spk) { line("(manual --set)", "—", "—"); continue }
            let thumb = outDir.appendingPathComponent("speakers/\(spk).jpg")
            guard let cg = FacesStage.loadCGImage(thumb), let fp = Fuse.featurePrint(cg) else {
                line("(no thumbnail)", "—", "—"); continue
            }
            var best: (character: String, dist: Float)? = nil
            for c in castFP {
                var d: Float = 0
                try? c.fp.computeDistance(&d, to: fp)
                if best == nil || d < best!.dist { best = (c.member.character, d) }
            }
            guard let b = best else { line("(no cast images)", "—", "—"); continue }
            if b.dist <= tmdbThreshold {
                result[spk] = b.character
                line("AUTO", b.character, String(format: "%.1f", b.dist))
            } else {
                line("low-conf → --set", b.character, String(format: "%.1f", b.dist))
            }
        }
        return result
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
