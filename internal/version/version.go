package version

import "fmt"

var (
	// Version is the release identifier injected at build time.
	Version = "development"
	// Commit is the git commit hash injected at build time.
	Commit = "unknown"
	// BuildDate is the RFC3339 UTC build timestamp injected at build time.
	BuildDate = "unknown"
)

// String returns a compact human-readable build identifier for CLI output.
func String() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}
	return fmt.Sprintf("%s (%s)", Version, Commit)
}
