import Foundation

// filmscan release version. Bump on each cut; scripts/publish-release.sh reads it
// (via `filmscan version`) as the polar-release version when publishing the binary,
// and the polar-agent fetcher compares it against a cached copy to decide whether
// to re-download. Keep it a plain literal so the build needs no codegen step.
let filmscanVersion = "0.1.0"
