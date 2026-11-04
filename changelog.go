// Package artex holds repository files that are embedded into the binary.
package artex

import _ "embed"

// Changelog is CHANGELOG.md as of the build, served to the UI so the version
// pill always shows the release notes of the running binary.
//
//go:embed CHANGELOG.md
var Changelog string
