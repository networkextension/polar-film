import ArgumentParser

// P3: name a speaker/face cluster so the attribution gets a real character +
// actor. Stub for now; the cluster ids come from the fuse stage (P2).
struct Label: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        abstract: "Name a speaker cluster (character/actor). [P3 — not implemented yet]"
    )

    func run() async throws {
        log("label: not implemented yet (P3 — needs diarize+fuse from P2 first).")
    }
}
