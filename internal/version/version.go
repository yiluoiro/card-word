// Package version holds build metadata for card word.
package version

// These values are overridden at build time via -ldflags.
var (
	Version = "v1.0.0"
	Commit  = "dev"
	Date    = "unknown"
)

// UserAgent is used for outbound requests.
func UserAgent() string { return "github.com/yiluoiro/card-word/" + Version }
