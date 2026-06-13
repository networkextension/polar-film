import ArgumentParser
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
                let n = try await client.uploadKeyframes(frames.frames, baseDir: outDir)
                log("push: \(n) keyframes uploaded")
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

/// Thin film-API client: Bearer + X-Workspace-Id, JSON subtitle upload and
/// multipart keyframe upload (batched).
struct FilmClient {
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

    /// Upload keyframes as multipart `file[]` + aligned `ts_ms[]`, in batches.
    func uploadKeyframes(_ frames: [Frame], baseDir: URL, batch: Int = 20) async throws -> Int {
        var sent = 0
        var i = 0
        while i < frames.count {
            let slice = Array(frames[i..<min(i + batch, frames.count)])
            let boundary = "filmscan-\(mediaID)-\(i)"
            var body = Data()
            for f in slice {
                let url = baseDir.appendingPathComponent(f.file)
                guard let bytes = try? Data(contentsOf: url) else { continue }
                appendPart(&body, boundary: boundary, name: "file",
                           filename: (f.file as NSString).lastPathComponent,
                           contentType: "image/jpeg", data: bytes)
                appendField(&body, boundary: boundary, name: "ts_ms", value: String(f.timeMs))
            }
            body.append("--\(boundary)--\r\n".data(using: .utf8)!)
            var req = request("api/film/movies/\(mediaID)/screenshots", method: "POST")
            req.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
            req.httpBody = body
            _ = try await send(req)
            sent += slice.count
            log("push:   keyframes \(sent)/\(frames.count)")
            i += batch
        }
        return sent
    }

    private func appendField(_ body: inout Data, boundary: String, name: String, value: String) {
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"\(name)\"\r\n\r\n".data(using: .utf8)!)
        body.append("\(value)\r\n".data(using: .utf8)!)
    }

    private func appendPart(_ body: inout Data, boundary: String, name: String, filename: String, contentType: String, data: Data) {
        body.append("--\(boundary)\r\n".data(using: .utf8)!)
        body.append("Content-Disposition: form-data; name=\"\(name)\"; filename=\"\(filename)\"\r\n".data(using: .utf8)!)
        body.append("Content-Type: \(contentType)\r\n\r\n".data(using: .utf8)!)
        body.append(data)
        body.append("\r\n".data(using: .utf8)!)
    }
}
