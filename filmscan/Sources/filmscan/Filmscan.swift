import ArgumentParser
import Foundation

// filmscan — produce speaker-attributed subtitles + keyframes from a video.
// See doc/speaker-subtitles.md. P0 = transcribe → SRT/JSON.
@main
struct Filmscan: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "filmscan",
        abstract: "Apple-native video analyzer for polar-film: speaker-attributed subtitles + keyframes.",
        version: filmscanVersion,
        subcommands: [Extract.self, Analyze.self, Label.self, Push.self],
        defaultSubcommand: Analyze.self
    )
}
