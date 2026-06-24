import Foundation

// Upload extracted keyframes into the workspace photo library.
// Server: POST /api/photo/assets/upload (multipart `file`); dedups by
// workspace+sha256, creates an Asset row. See
// modules/polar-photo/internal/photo/assets_handlers.go.
//
// We upload the full keyframe JPEGs (not just face crops): the photo module runs
// its own face detection/clustering, and the extract manifest separately carries
// our Vision face boxes + 768-d feature-prints so the analyze/identity tier can
// re-ID without re-decoding the frames.
struct PhotoClient: Sendable {
    let http: PolarHTTP

    init(base: URL, token: String, workspaceID: String?) {
        self.http = PolarHTTP(base: base, token: token, workspaceID: workspaceID)
    }

    /// Upload one image file; returns its photo-library asset id (deduped if the
    /// same bytes already exist in this workspace).
    func uploadKeyframe(fileURL: URL) async throws -> String {
        var lastErr: Error?
        for attempt in 0..<3 {
            do {
                let req = try http.multipartUpload("api/photo/assets/upload",
                                                   fields: ["type": "photo"],
                                                   fileField: "file",
                                                   fileName: fileURL.lastPathComponent,
                                                   mime: "image/jpeg",
                                                   fileURL: fileURL)
                let data = try await http.send(req)
                guard let id = extractID(data, wrapperKeys: ["asset"]) else {
                    throw Err("uploadKeyframe: no id in response: \(String(data: data, encoding: .utf8) ?? "")")
                }
                return id
            } catch {
                lastErr = error
                if attempt < 2 { try? await Task.sleep(nanoseconds: UInt64(attempt + 1) * 500_000_000) }
            }
        }
        throw lastErr ?? Err("uploadKeyframe failed")
    }
}
