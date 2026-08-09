package repository

import (
	"errors"
	"testing"

	models "github.com/rebusman/svcmetrics/internal/model"
)

func gauge(name string, value float64) models.Metrics {
	return models.Metrics{ID: name, MType: models.Gauge, Value: &value}
}

func counter(name string, delta int64) models.Metrics {
	return models.Metrics{ID: name, MType: models.Counter, Delta: &delta}
}

func TestAggregateBatchFoldsDuplicates(t *testing.T) {
	batch, err := aggregateBatch([]models.Metrics{
		gauge("Alloc", 1),
		counter("PollCount", 2),
		gauge("Alloc", 9),
		counter("PollCount", 5),
	})
	if err != nil {
		t.Fatalf("aggregateBatch error = %v", err)
	}

	if len(batch) != 2 {
		t.Fatalf("batch has %d entries, want 2", len(batch))
	}
	if batch[0].ID != "PollCount" || *batch[0].Delta != 7 {
		t.Errorf("counter = %+v, want PollCount with delta 7", batch[0])
	}
	if batch[1].ID != "Alloc" || *batch[1].Value != 9 {
		t.Errorf("gauge = %+v, want Alloc with the last value 9", batch[1])
	}
}

// TestAggregateBatchKeepsTypesApart verifies that a name existing as both a
// gauge and a counter is not folded into one entry.
func TestAggregateBatchKeepsTypesApart(t *testing.T) {
	batch, err := aggregateBatch([]models.Metrics{
		gauge("Same", 1.5),
		counter("Same", 3),
	})
	if err != nil {
		t.Fatalf("aggregateBatch error = %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("batch has %d entries, want 2", len(batch))
	}
}

// TestAggregateBatchOrdersMetrics verifies the fixed write order: without it
// two concurrent batches could take row locks in opposite order and deadlock.
func TestAggregateBatchOrdersMetrics(t *testing.T) {
	batch, err := aggregateBatch([]models.Metrics{
		gauge("Sys", 1),
		counter("PollCount", 1),
		gauge("Alloc", 1),
		counter("Errors", 1),
	})
	if err != nil {
		t.Fatalf("aggregateBatch error = %v", err)
	}

	want := []string{"Errors", "PollCount", "Alloc", "Sys"}
	for i, name := range want {
		if batch[i].ID != name {
			t.Fatalf("batch order = %v, want %v", ids(batch), want)
		}
	}
}

func ids(batch []models.Metrics) []string {
	out := make([]string, 0, len(batch))
	for _, m := range batch {
		out = append(out, m.ID)
	}
	return out
}

func TestAggregateBatchRejectsMalformedMetrics(t *testing.T) {
	value := 1.5
	delta := int64(1)

	tests := []struct {
		name  string
		batch []models.Metrics
	}{
		{name: "missing id", batch: []models.Metrics{{MType: models.Gauge, Value: &value}}},
		{name: "unknown type", batch: []models.Metrics{{ID: "X", MType: "histogram", Value: &value}}},
		{name: "empty type", batch: []models.Metrics{{ID: "X", Value: &value}}},
		{name: "gauge without value", batch: []models.Metrics{{ID: "X", MType: models.Gauge}}},
		{name: "counter without delta", batch: []models.Metrics{{ID: "X", MType: models.Counter}}},
		{name: "gauge carrying only a delta", batch: []models.Metrics{{ID: "X", MType: models.Gauge, Delta: &delta}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := aggregateBatch(tt.batch); !errors.Is(err, models.ErrInvalidMetric) {
				t.Fatalf("error = %v, want ErrInvalidMetric", err)
			}
		})
	}
}

// TestAggregateBatchCopiesValues verifies that the aggregated batch shares no
// pointers with the input, which the caller keeps using after the write.
func TestAggregateBatchCopiesValues(t *testing.T) {
	input := []models.Metrics{gauge("Alloc", 1), counter("PollCount", 2)}

	batch, err := aggregateBatch(input)
	if err != nil {
		t.Fatalf("aggregateBatch error = %v", err)
	}

	for i := range batch {
		switch batch[i].MType {
		case models.Gauge:
			*batch[i].Value = 100
		case models.Counter:
			*batch[i].Delta = 100
		}
	}

	if *input[0].Value != 1 {
		t.Errorf("input gauge changed to %v, want it untouched", *input[0].Value)
	}
	if *input[1].Delta != 2 {
		t.Errorf("input counter changed to %v, want it untouched", *input[1].Delta)
	}
}
