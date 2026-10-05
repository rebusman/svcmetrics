package agent

import (
	"testing"

	models "github.com/rebusman/svcmetrics/internal/model"
)

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
		a.mu.Lock()
		a.metrics.counters[models.PollCount]++
		a.mu.Unlock()

		if batch := a.collectBatch(); len(batch) == 0 {
			b.Fatal("empty batch")
		}
	}
}
