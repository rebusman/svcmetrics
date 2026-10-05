package repository

import (
	"context"
	"strconv"
	"testing"

	models "github.com/rebusman/svcmetrics/internal/model"
)

// benchBatch builds a batch shaped like the one the agent sends: gauges and a
// single counter.
func benchBatch(n int) []models.Metrics {
	batch := make([]models.Metrics, 0, n)
	for i := range n - 1 {
		value := float64(i) * 1.5
		batch = append(batch, models.Metrics{ID: "Gauge" + strconv.Itoa(i), MType: models.Gauge, Value: &value})
	}
	delta := int64(1)
	return append(batch, models.Metrics{ID: models.PollCount, MType: models.Counter, Delta: &delta})
}

func BenchmarkMemStorageUpdateGauge(b *testing.B) {
	s := NewMemStorage()
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.UpdateGauge(ctx, "Alloc", 1.5); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMemStorageUpdateCounter(b *testing.B) {
	s := NewMemStorage()
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.UpdateCounter(ctx, models.PollCount, 1); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMemStorageUpdateBatch(b *testing.B) {
	s := NewMemStorage()
	ctx := context.Background()
	batch := benchBatch(32)

	b.ReportAllocs()
	for b.Loop() {
		if err := s.UpdateBatch(ctx, batch); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMemStorageGetAllGauges(b *testing.B) {
	s := NewMemStorage()
	ctx := context.Background()
	if err := s.UpdateBatch(ctx, benchBatch(64)); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := s.GetAllGauges(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAggregateBatch(b *testing.B) {
	batch := benchBatch(32)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := aggregateBatch(batch); err != nil {
			b.Fatal(err)
		}
	}
}
