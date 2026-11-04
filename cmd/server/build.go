package main

import (
	"fmt"
	"io"
)

// Build information, set at link time:
//
//	go build -ldflags "-X main.buildVersion=v1.0.0 -X main.buildDate=$(date +%F) -X main.buildCommit=$(git rev-parse --short HEAD)"
var (
	buildVersion string
	buildDate    string
	buildCommit  string
)

// printBuildInfo writes the build information to w, with N/A in place of a
// value that was not set at link time.
func printBuildInfo(w io.Writer) {
	_, _ = fmt.Fprintf(w, "Build version: %s\n", valueOrNA(buildVersion))
	_, _ = fmt.Fprintf(w, "Build date: %s\n", valueOrNA(buildDate))
	_, _ = fmt.Fprintf(w, "Build commit: %s\n", valueOrNA(buildCommit))
}

func valueOrNA(v string) string {
	if v == "" {
		return "N/A"
	}
	return v
}
