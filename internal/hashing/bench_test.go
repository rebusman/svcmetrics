package hashing

import (
	"bytes"
	"testing"
)

// benchBody is about the size of a compressed batch the agent sends.
var benchBody = bytes.Repeat([]byte("metric"), 200)

func BenchmarkSum(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = Sum(benchBody, "key")
	}
}

func BenchmarkEqual(b *testing.B) {
	sig := Sum(benchBody, "key")

	b.ReportAllocs()
	for b.Loop() {
		if !Equal(sig, benchBody, "key") {
			b.Fatal("signature mismatch")
		}
	}
}
