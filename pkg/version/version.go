package version

// Version contains the binary version injected by the build system via ldflags
var Version string

// GitCommit contains the git commit sha that the binary was built with, injected by the build system via ldflags
var GitCommit string

// GetVersion returns the version string with fallback logic:
// 1. If Version is set via ldflags, use it
// 2. Otherwise, use v0.1.0 as default
// 3. Append commit hash if available
func GetVersion() string {
	version := Version
	commit := GitCommit

	// Default version if not set via ldflags
	if version == "" {
		version = "v0.1.0"
	}

	// Append commit hash if available
	if commit != "" {
		// Truncate commit to 7 characters
		if len(commit) > 7 {
			commit = commit[:7]
		}
		return version + "-" + commit
	}

	return version
}
