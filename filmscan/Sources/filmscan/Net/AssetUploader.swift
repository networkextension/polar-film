import Foundation

// Shared HTTP plumbing for the Polar dock API: Bearer + X-Workspace-Id auth,
// JSON + multipart helpers, and retry/backoff. Factored out of Commands/Push.swift's
// FilmClient so the extract-stage clients (MusicClient, PhotoClient) reuse the exact
// same auth + retry behavior instead of re-implementing it.
struct PolarHTTP: Sendable {
    let base: URL
    let token: String
    let workspaceID: String?

    func request(_ path: String, method: String) -> URLRequest {
        var req = URLRequest(url: base.appendingPathComponent(path))
        req.httpMethod = method
        req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        if let ws = workspaceID, !ws.isEmpty { req.setValue(ws, forHTTPHeaderField: "X-Workspace-Id") }
        return req
    }

    @discardableResult
    func send(_ req: URLRequest) async throws -> Data {
        let (data, resp) = try await URLSession.shared.data(for: req)
        guard let http = resp as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            let code = (resp as? HTTPURLResponse)?.statusCode ?? -1
            let body = String(data: data, encoding: .utf8) ?? ""
            throw Err("HTTP \(code): \(body)")
        }
        return data
    }

    /// Build a multipart/form-data request with optional text fields plus one file part.
    func multipartUpload(_ path: String,
                         fields: [String: String],
                         fileField: String,
                         fileName: String,
                         mime: String,
                         fileURL: URL) throws -> URLRequest {
        let boundary = "polarfilmscan" + UUID().uuidString
        var body = Data()
        func append(_ s: String) { body.append(s.data(using: .utf8)!) }

        for (k, v) in fields {
            append("--\(boundary)\r\n")
            append("Content-Disposition: form-data; name=\"\(k)\"\r\n\r\n")
            append("\(v)\r\n")
        }
        let fileData = try Data(contentsOf: fileURL)
        append("--\(boundary)\r\n")
        append("Content-Disposition: form-data; name=\"\(fileField)\"; filename=\"\(fileName)\"\r\n")
        append("Content-Type: \(mime)\r\n\r\n")
        body.append(fileData)
        append("\r\n--\(boundary)--\r\n")

        var req = request(path, method: "POST")
        req.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        req.httpBody = body
        return req
    }

    func withRetry(_ tries: Int = 3, _ op: @Sendable () async throws -> Void) async throws {
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

/// Pull a string `id` out of a JSON object, tolerating either a top-level `id`
/// or a wrapper like `{"track": {...}}` / `{"asset": {...}}`. Ids may be numeric
/// or string server-side, so we normalize to String.
func extractID(_ data: Data, wrapperKeys: [String]) -> String? {
    guard let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
    func idIn(_ obj: [String: Any]) -> String? {
        if let s = obj["id"] as? String { return s }
        if let n = obj["id"] as? Int { return String(n) }
        if let n = obj["id"] as? Int64 { return String(n) }
        if let d = obj["id"] as? Double { return String(Int64(d)) }
        return nil
    }
    if let s = idIn(root) { return s }
    for k in wrapperKeys {
        if let sub = root[k] as? [String: Any], let s = idIn(sub) { return s }
    }
    return nil
}

/// Pull an Int64 field (e.g. "audio_asset_id") out of a JSON object, tolerating a
/// top-level field or a wrapper like {"track": {...}}. Numeric server-side.
func extractInt64Field(_ data: Data, wrapperKeys: [String], field: String) -> Int64? {
    guard let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
    func val(_ obj: [String: Any]) -> Int64? {
        if let n = obj[field] as? Int64 { return n }
        if let n = obj[field] as? Int { return Int64(n) }
        if let d = obj[field] as? Double { return Int64(d) }
        if let s = obj[field] as? String { return Int64(s) }
        return nil
    }
    if let n = val(root) { return n }
    for k in wrapperKeys {
        if let sub = root[k] as? [String: Any], let n = val(sub) { return n }
    }
    return nil
}
