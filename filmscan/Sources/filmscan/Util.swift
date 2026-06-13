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
