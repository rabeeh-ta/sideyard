// Package version identifies this build of Sideyard. Release builds set
// both values with -ldflags "-X sideyard/internal/version.Version=v1.2.3
// -X sideyard/internal/version.Source=https://github.com/owner/repo".
package version

var (
	// Version is the release, like v1.2.3, or "dev" for a local build.
	Version = "dev"
	// Source is where releases are published, laid out like GitHub
	// Releases. Empty in local builds, which then don't check for updates.
	Source = ""
)
