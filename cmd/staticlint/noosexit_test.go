package main

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestNoOsExitAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), noOsExitAnalyzer, "exitmain", "exitlib", "exitgen")
}
