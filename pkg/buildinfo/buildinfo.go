// Package buildinfo carries build-time metadata injected via -ldflags.
package buildinfo

// Version is the semantic version of this build. It is overridden at build
// time from the VERSION file:
//
//	-ldflags "-X github.com/AxelTahmid/tinker/pkg/buildinfo.Version=$(cat VERSION)"
//
// Releases are cut with `make release`, which bumps VERSION, regenerates
// CHANGELOG.md, and tags the commit.
var Version = "dev"
