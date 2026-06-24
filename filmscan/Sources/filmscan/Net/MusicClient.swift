import Foundation

// Upload the extracted audio track to the workspace music library.
// Server: POST /api/tracks (multipart `file` + optional `duration_ms`); dedups by
// workspace+sha256, parses ID3/metadata, stores via Dock.AssetUpload(Kind=media).
// See modules/polar-music/internal/music/tracks_handlers.go.
struct MusicClient: Sendable {
    let http: PolarHTTP

    init(base: URL, token: String, workspaceID: String?) {
        self.http = PolarHTTP(base: base, token: token, workspaceID: workspaceID)
    }

    /// Upload an audio file; returns the server track id (deduped to the existing
    /// track if the same bytes were already uploaded in this workspace).
    func uploadTrack(fileURL: URL, durationMs: Int?, title: String) async throws -> String {
        var fields: [String: String] = [:]
        if let d = durationMs, d > 0 { fields["duration_ms"] = String(d) }
        let mime = mimeForAudio(fileURL)
        var lastErr: Error?
        for attempt in 0..<3 {
            do {
                let req = try http.multipartUpload("api/tracks",
                                                   fields: fields,
                                                   fileField: "file",
                                                   fileName: fileURL.lastPathComponent,
                                                   mime: mime,
                                                   fileURL: fileURL)
                let data = try await http.send(req)
                guard let id = extractID(data, wrapperKeys: ["track"]) else {
                    throw Err("uploadTrack: no id in response: \(String(data: data, encoding: .utf8) ?? "")")
                }
                return id
            } catch {
                lastErr = error
                if attempt < 2 { try? await Task.sleep(nanoseconds: UInt64(attempt + 1) * 500_000_000) }
            }
        }
        throw lastErr ?? Err("uploadTrack failed")
    }

    /// Resolve a track's signed audio URL via GET /api/tracks/:id/stream-url
    /// (the JSON-wrapped variant; avoids following a 302 ourselves).
    func streamURL(trackID: String) async throws -> URL {
        let req = http.request("api/tracks/\(trackID)/stream-url", method: "GET")
        let data = try await http.send(req)
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let s = (obj["url"] as? String) ?? (obj["stream_url"] as? String),
              let u = URL(string: s) else {
            throw Err("streamURL: no url in response: \(String(data: data, encoding: .utf8) ?? "")")
        }
        return u
    }

    /// Download a track's audio bytes to `dest` (used by the analyze stage to pull
    /// the audio back when it runs on a different box than extract).
    func downloadAudio(trackID: String, to dest: URL) async throws {
        let url = try await streamURL(trackID: trackID)
        let (tmp, resp) = try await URLSession.shared.download(from: url)
        guard let http = resp as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            throw Err("downloadAudio HTTP \((resp as? HTTPURLResponse)?.statusCode ?? -1)")
        }
        try? FileManager.default.removeItem(at: dest)
        try FileManager.default.moveItem(at: tmp, to: dest)
    }

    private func mimeForAudio(_ url: URL) -> String {
        switch url.pathExtension.lowercased() {
        case "mp3": return "audio/mpeg"
        case "m4a", "aac": return "audio/mp4"
        case "wav": return "audio/wav"
        case "flac": return "audio/flac"
        default: return "application/octet-stream"
        }
    }
}
