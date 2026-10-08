package agent

import "testing"

func BenchmarkCollectRuntimeMetrics(b *testing.B) {
	a := New("", 0, 0, 0, "", 0)

	b.ReportAllocs()
	for b.Loop() {
		a.CollectRuntimeMetrics()
	}
}

func BenchmarkCollectBatch(b *testing.B) {
	a := New("", 0, 0, 0, "", 0)
	a.CollectRuntimeMetrics()

	b.ReportAllocs()
	for b.Loop() {
		// A poll between reports gives PollCount a non-zero delta, so every
		// batch carries a counter as well as the gauges. The poll itself is
		// measured by BenchmarkCollectRuntimeMetrics, not here.
		b.StopTimer()
		a.CollectRuntimeMetrics()
		b.StartTimer()

		if batch := a.collectBatch(); len(batch) == 0 {
			b.Fatal("empty batch")
		}
	}
}
