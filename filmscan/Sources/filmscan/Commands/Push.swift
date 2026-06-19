import ArgumentParser
import CryptoKit
import Foundation

// P4c — upload an analyzed bundle to the polar-film knowledge base. filmscan is
// a macOS-only binary (Vision/AVFoundation/WhisperKit); the film service runs on
// Linux, so it can't shell out to us. Instead this runs on the Mac next to
// `analyze` and POSTs the results to the film HTTP API:
//   • the SRT (with "[Speaker]" tags) → POST /api/film/movies/:id/subtitles
//     (the server parses the tags into segments + people — see P4a/P4b)
//   • each keyframe JPEG → POST /api/film/movies/:id/screenshots (multipart)
// Auth is the same Bearer token + X-Workspace-Id the rest of the API uses.
struct Push: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Upload an analyzed .filmscan bundle (subtitles + keyframes) to a polar-film movie."
    )

    @Argument(help: "The .filmscan output directory produced by `analyze`.")
    var out: String

    @Option(name: .long, help: "Film server base URL (or env FILMSCAN_SERVER), e.g. https://film.4950.store.")
    var server: String?

    @Option(name: .long, help: "Bearer access token (or env FILMSCAN_TOKEN).")
    var token: String?

    @Option(name: .long, help: "Target movie/media id in polar-film.")
    var mediaID: String

    @Option(name: .long, help: "Workspace id (X-Workspace-Id); omit for personal.")
    var workspaceID: String?

    @Option(name: .long, help: "Subtitle language code.")
    var lang: String = "en"

    @Flag(name: .long, help: "Skip uploading the SRT.")
    var noSubtitles: Bool = false

    @Flag(name: .long, help: "Skip uploading keyframes.")
    var noScreenshots: Bool = false

    func run() async throws {
        let outDir = URL(fileURLWithPath: out)
        let base = (server ?? ProcessInfo.processInfo.environment["FILMSCAN_SERVER"] ?? "")
            .trimmingCharacters(in: .whitespaces)
        guard let baseURL = URL(string: base), base.hasPrefix("http") else {
            throw ValidationError("--server (or FILMSCAN_SERVER) must be an http(s) URL")
        }
        let tok = (token ?? ProcessInfo.processInfo.environment["FILMSCAN_TOKEN"] ?? "")
            .trimmingCharacters(in: .whitespaces)
        guard !tok.isEmpty else { throw ValidationError("--token (or FILMSCAN_TOKEN) is required") }

        let client = FilmClient(base: baseURL, token: tok, workspaceID: workspaceID, mediaID: mediaID)

        if !noSubtitles {
            guard let srt = try findSRT(in: outDir) else {
                log("push: no .srt in \(out) — run `analyze` first (or pass --no-subtitles)")
                throw ExitCode.failure
            }
            let content = try String(contentsOf: srt, encoding: .utf8)
            let n = try await client.uploadSubtitle(lang: lang, content: content)
            log("push: subtitles uploaded — \(n) segments")
        }

        if !noScreenshots {
            let framesURL = outDir.appendingPathComponent("frames.json")
            if let frames = try? loadJSON(Frames.self, from: framesURL), !frames.frames.isEmpty {
                let r = try await client.uploadKeyframes(frames.frames, baseDir: outDir)
                log("push: keyframes done — \(r.uploaded) uploaded direct, \(r.deduped) deduped")
            } else {
                log("push: no frames.json — skipping keyframes")
            }
        }
        log("push: done → movie \(mediaID)")
    }

    /// Find the emitted subtitle file in the bundle (there is one `<stem>.srt`).
    private func findSRT(in dir: URL) throws -> URL? {
        let items = (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? []
        return items.first { $0.pathExtension.lowercased() == "srt" }
    }
}

// Wire types for the direct-to-storage keyframe upload (snake_case to match
// the film API JSON directly).
private struct GrantReqItem: Codable { let sha256: String; let size: Int; let mime: String; let ext: String; let ts_ms: Int }
private struct GrantReq: Codable { let items: [GrantReqItem] }
private struct GrantRespItem: Codable {
    let sha256: String; let screenshot_id: String
    let asset_id: Int; let provider_id: Int; let put_url: String; let exists: Bool
}
private struct GrantResp: Codable { let grants: [GrantRespItem] }
private struct CommitReqItem: Codable {
    let screenshot_id: String; let asset_id: Int; let provider_id: Int
    let ts_ms: Int; let phash: String; let exists: Bool
}
private struct CommitReq: Codable { let items: [CommitReqItem] }

/// Thin film-API client: Bearer + X-Workspace-Id, JSON subtitle upload and
/// direct-to-storage keyframe upload (grant → PUT-to-provider → commit).
struct FilmClient: Sendable {
    let base: URL
    let token: String
    let workspaceID: String?
    let mediaID: String

    private func request(_ path: String, method: String) -> URLRequest {
        var req = URLRequest(url: base.appendingPathComponent(path))
        req.httpMethod = method
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        if let ws = workspaceID, !ws.isEmpty { req.setValue(ws, forHTTPHeaderField: "X-Workspace-Id") }
        return req
    }

    private func send(_ req: URLRequest) async throws -> Data {
        let (data, resp) = try await URLSession.shared.data(for: req)
        guard let http = resp as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            let code = (resp as? HTTPURLResponse)?.statusCode ?? -1
            let body = String(data: data, encoding: .utf8) ?? ""
            throw Err("HTTP \(code): \(body)")
        }
        return data
    }

    /// POST the SRT; returns the server's parsed segment count.
    func uploadSubtitle(lang: String, content: String) async throws -> Int {
        var req = request("api/film/movies/\(mediaID)/subtitles", method: "POST")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(["lang": lang, "format": "srt", "content": content])
        let data = try await send(req)
        let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        return (obj?["segments"] as? Int) ?? 0
    }

    /// Upload keyframes direct to storage: per batch, hash locally, ask film
    /// for upload grants, PUT the misses straight to the assets provider
    /// (bytes never transit film-svc), then commit the refs. Returns counts of
    /// (newly uploaded, deduped/skipped).
    func uploadKeyframes(_ frames: [Frame], baseDir: URL, batch: Int = 100, concurrency: Int = 6) async throws -> (uploaded: Int, deduped: Int) {
        var uploaded = 0, deduped = 0, done = 0
        var i = 0
        while i < frames.count {
            let slice = Array(frames[i..<min(i + batch, frames.count)])

            // 1. Local metadata: bytes, sha256, size, ext. (order-aligned)
            // (phash is left to a future server-side backfill — see note below.)
            var locals: [(frame: Frame, bytes: Data, sha: String, ext: String)] = []
            var items: [GrantReqItem] = []
            for f in slice {
                let url = baseDir.appendingPathComponent(f.file)
                guard let bytes = try? Data(contentsOf: url) else { continue }
                let sha = SHA256.hash(data: bytes).map { String(format: "%02x", $0) }.joined()
                var ext = "." + (f.file as NSString).pathExtension.lowercased()
                if ext == "." { ext = ".jpg" }
                locals.append((f, bytes, sha, ext))
                items.append(GrantReqItem(sha256: sha, size: bytes.count, mime: "image/jpeg", ext: ext, ts_ms: f.timeMs))
            }
            if locals.isEmpty { i += batch; continue }

            // 2. Grants (order-aligned with locals).
            let grants = try await requestGrants(items)
            guard grants.count == locals.count else {
                throw Err("grant count mismatch: \(grants.count) != \(locals.count)")
            }

            // 3. PUT the misses straight to the provider, bounded-concurrent + retried.
            let misses: [(idx: Int, url: String)] = grants.enumerated()
                .filter { !$0.element.exists && !$0.element.put_url.isEmpty }
                .map { ($0.offset, $0.element.put_url) }
            var j = 0
            while j < misses.count {
                let chunk = Array(misses[j..<min(j + concurrency, misses.count)])
                try await withThrowingTaskGroup(of: Void.self) { group in
                    for m in chunk {
                        let bytes = locals[m.idx].bytes
                        let url = m.url
                        group.addTask { try await self.withRetry { try await self.putBlob(url, data: bytes) } }
                    }
                    try await group.waitForAll()
                }
                j += concurrency
            }

            // 4. Commit refs (records rows; idempotent server-side by media+asset).
            // phash left empty: perceptual hashing is deferred to a uniform
            // server-side backfill (CoreGraphics vs Go's image/jpeg can't be
            // made byte-identical, and the column is advisory/M4+).
            let commitItems = grants.enumerated().map { (k, g) in
                CommitReqItem(screenshot_id: g.screenshot_id, asset_id: g.asset_id,
                              provider_id: g.provider_id, ts_ms: locals[k].frame.timeMs,
                              phash: "", exists: g.exists)
            }
            try await commitScreenshots(commitItems)

            uploaded += misses.count
            deduped += grants.count - misses.count
            done += grants.count
            log("push:   keyframes \(done)/\(frames.count) (uploaded \(uploaded), dup \(deduped))")
            i += batch
        }
        return (uploaded, deduped)
    }

    private func requestGrants(_ items: [GrantReqItem]) async throws -> [GrantRespItem] {
        var req = request("api/film/movies/\(mediaID)/screenshots/grants", method: "POST")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(GrantReq(items: items))
        let data = try await send(req)
        return try JSONDecoder().decode(GrantResp.self, from: data).grants
    }

    private func commitScreenshots(_ items: [CommitReqItem]) async throws {
        var req = request("api/film/movies/\(mediaID)/screenshots/commit", method: "POST")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(CommitReq(items: items))
        _ = try await send(req)
    }

    /// PUT bytes straight to a dock-signed provider URL (no auth header — the
    /// signature is in the URL). This is the byte path that bypasses film-svc.
    private func putBlob(_ urlStr: String, data: Data) async throws {
        guard let u = URL(string: urlStr) else { throw Err("bad put_url") }
        var req = URLRequest(url: u)
        req.httpMethod = "PUT"
        req.setValue("image/jpeg", forHTTPHeaderField: "Content-Type")
        req.httpBody = data
        let (rdata, resp) = try await URLSession.shared.data(for: req)
        guard let http = resp as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            let code = (resp as? HTTPURLResponse)?.statusCode ?? -1
            throw Err("PUT \(code): \(String(data: rdata, encoding: .utf8) ?? "")")
        }
    }

    private func withRetry(_ tries: Int = 3, _ op: @Sendable () async throws -> Void) async throws {
        var last: Error?
        for attempt in 0..<tries {
            do { try await op(); return }
            catch {
                last = error
                if attempt < tries - 1 {
                    try? await Task.sleep(nanoseconds: UInt64(attempt + 1) * 500_000_000)
                }
            }
        }
        throw last ?? Err("retry exhausted")
    }
}
