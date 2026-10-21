package main

import (
	"bytes"
	"testing"
)

func TestPrintBuildInfo(t *testing.T) {
	defer func(v, d, c string) { buildVersion, buildDate, buildCommit = v, d, c }(buildVersion, buildDate, buildCommit)

	tests := []struct {
		name                  string
		version, date, commit string
		want                  string
	}{
		{
			name: "unset",
			want: "Build version: N/A\nBuild date: N/A\nBuild commit: N/A\n",
		},
		{
			name:    "set",
			version: "v1.0.0", date: "2026-10-21", commit: "abc1234",
			want: "Build version: v1.0.0\nBuild date: 2026-10-21\nBuild commit: abc1234\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buildVersion, buildDate, buildCommit = tt.version, tt.date, tt.commit
			var buf bytes.Buffer
			printBuildInfo(&buf)
			if got := buf.String(); got != tt.want {
				t.Fatalf("printBuildInfo() = %q, want %q", got, tt.want)
			}
		})
	}
}
