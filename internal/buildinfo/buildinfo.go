// Package buildinfo holds values injected at link time with -ldflags -X.
package buildinfo

// Version is set with -X github.com/retreat-community/lanscape/internal/buildinfo.Version=v1.2.3.
var Version = "dev"

// Commit is the git commit.
var Commit = ""
