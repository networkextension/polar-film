import Foundation
import CoreGraphics
import ImageIO

// P3 — pull a movie's cast from TMDB so `label` can auto-name speaker clusters.
// We fetch the credits (character names + actor profile photos + billing order)
// and download each profile image; the matcher in Label compares those against
// the spkN.jpg thumbnails via Vision feature-print.
//
// Network: TMDB API + image.tmdb.org are often blocked in CN and macOS
// URLSession ignores http_proxy env — use --tmdb-cast with a pre-saved credits
// JSON when direct fetch isn't possible.
enum TMDB {
    struct CastMember: Codable {
        var name: String          // actor
        var character: String     // role
        var profilePath: String?  // /abc.jpg under image.tmdb.org
        var order: Int            // billing order (0 = top-billed)

        enum CodingKeys: String, CodingKey {
            case name, character, order
            case profilePath = "profile_path"
        }
    }

    private struct Credits: Codable { var cast: [CastMember] }

    /// Fetch `/3/movie/{id}/credits` cast, sorted by billing order.
    static func fetchCredits(id: Int, key: String) async throws -> [CastMember] {
        guard let url = URL(string: "https://api.themoviedb.org/3/movie/\(id)/credits?api_key=\(key)") else {
            throw Err("bad TMDB url")
        }
        let (data, resp) = try await URLSession.shared.data(from: url)
        guard let http = resp as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            let code = (resp as? HTTPURLResponse)?.statusCode ?? -1
            throw Err("TMDB credits HTTP \(code) (check --tmdb-id / --tmdb-key / network)")
        }
        return try JSONDecoder().decode(Credits.self, from: data).cast.sorted { $0.order < $1.order }
    }

    /// Load a pre-saved credits JSON (the raw `/credits` response, or a bare
    /// `{"cast":[…]}`) for offline use. profile_path may be a local file path.
    static func loadCredits(file: URL) throws -> [CastMember] {
        let data = try Data(contentsOf: file)
        return try JSONDecoder().decode(Credits.self, from: data).cast.sorted { $0.order < $1.order }
    }

    /// Resolve a cast member's profile image to a CGImage. A `profile_path` that
    /// is an absolute/relative local file is loaded directly (offline fixtures);
    /// otherwise it's fetched from image.tmdb.org.
    static func profileImage(_ path: String, baseDir: URL) async -> CGImage? {
        // local file (offline fixture)?
        let local = path.hasPrefix("/") ? URL(fileURLWithPath: path) : baseDir.appendingPathComponent(path)
        if FileManager.default.fileExists(atPath: local.path) {
            return FacesStage.loadCGImage(local)
        }
        guard let url = URL(string: "https://image.tmdb.org/t/p/w185\(path)"),
              let (data, _) = try? await URLSession.shared.data(from: url),
              let src = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        return CGImageSourceCreateImageAtIndex(src, 0, nil)
    }
}
