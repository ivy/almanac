// Package version holds the version an almanac binary reports, set at link
// time by the Makefile.
package version

// Version is overwritten at link time with -ldflags "-X github.com/ivy/almanac/version.Version=...".
var Version = "dev"

// String is the version a binary prints.
func String() string { return Version }
