// Package buildinfo holds the release version, stamped by GoReleaser at
// build time via -ldflags "-X github.com/DERYK-RYLOY/creator/internal/buildinfo.Version=x.y.z".
// Local builds keep the default.
package buildinfo

// Version is the semantic version without a leading v (e.g. "0.1.0").
var Version = "0.1.0"
