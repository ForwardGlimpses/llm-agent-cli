// Package version holds build-time version metadata.
//
// The default values are used by `go run` and local development. Release
// builds inject the real values with -ldflags, for example:
//
//	go build -ldflags "-X agentcli/internal/version.Version=0.2.0 \
//	  -X agentcli/internal/version.Commit=$(git rev-parse --short HEAD)" \
//	  -o bin/agent.exe ./cmd/agent
package version

import "fmt"

var (
	// Version is the semantic version without the leading "v".
	Version = "0.1.0"
	// Commit is the short git commit, or "dev" for local builds.
	Commit = "dev"
	// Date is the build date, or "unknown" for local builds.
	Date = "unknown"
)

// String returns "0.2.0 (abc1234)" for release builds and "0.1.0" for local
// development builds.
func String() string {
	if Commit == "" || Commit == "dev" {
		return Version
	}
	return fmt.Sprintf("%s (%s)", Version, Commit)
}
