import Foundation

func log(_ s: String) {
    FileHandle.standardError.write(("filmscan: " + s + "\n").data(using: .utf8)!)
}

func saveJSON<T: Encodable>(_ value: T, to url: URL) throws {
    let enc = JSONEncoder()
    enc.outputFormatting = [.prettyPrinted, .sortedKeys]
    try enc.encode(value).write(to: url, options: .atomic)
}

func loadJSON<T: Decodable>(_ type: T.Type, from url: URL) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(contentsOf: url))
}

// Run an external process to completion, capturing combined stdout+stderr (drained
// so a verbose child like ffmpeg can't deadlock on a full pipe buffer). Returns
// (exit status, captured output). Throws only if the binary fails to launch.
@discardableResult
func runProcess(_ launchPath: String, _ args: [String]) throws -> (status: Int32, output: String) {
    let p = Process()
    p.executableURL = URL(fileURLWithPath: launchPath)
    p.arguments = args
    let pipe = Pipe()
    p.standardOutput = pipe
    p.standardError = pipe
    try p.run()
    let data = pipe.fileHandleForReading.readDataToEndOfFile()
    p.waitUntilExit()
    return (p.terminationStatus, String(data: data, encoding: .utf8) ?? "")
}
