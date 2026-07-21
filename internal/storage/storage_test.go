package storage

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	models "github.com/rebusman/svcmetrics/internal/model"
)

func TestMemStorageCounterAccumulates(t *testing.T) {
	s := NewMemStorage()

	s.UpdateCounter("PollCount", 1)
	s.UpdateCounter("PollCount", 2)

	got, err := s.GetCounter("PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if got != 3 {
		t.Fatalf("counter value = %d, want 3", got)
	}
}

func TestMemStorageMissingMetrics(t *testing.T) {
	s := NewMemStorage()

	if _, err := s.GetGauge("missing"); err == nil {
		t.Fatal("GetGauge error = nil, want error")
	}
	if _, err := s.GetCounter("missing"); err == nil {
		t.Fatal("GetCounter error = nil, want error")
	}
}

func TestMemStorageSaveLoadRoundTrip(t *testing.T) {
	s := NewMemStorage()
	s.UpdateGauge("Alloc", 12.5)
	s.UpdateCounter("PollCount", 3)

	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}

	var metrics []models.Metrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		t.Fatalf("json.Unmarshal error = %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("metrics count = %d, want 2", len(metrics))
	}

	loaded := NewMemStorage()
	if err := loaded.Load(path); err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if got, err := loaded.GetGauge("Alloc"); err != nil || got != 12.5 {
		t.Fatalf("loaded gauge = %v, err = %v, want 12.5, nil", got, err)
	}
	if got, err := loaded.GetCounter("PollCount"); err != nil || got != 3 {
		t.Fatalf("loaded counter = %d, err = %v, want 3, nil", got, err)
	}
}

func TestMemStorageSaveLoadLargeCounter(t *testing.T) {
	s := NewMemStorage()
	const bigCounter = int64(math.MaxInt64 - 1)
	s.UpdateCounter("PollCount", bigCounter)

	path := filepath.Join(t.TempDir(), "metrics_large_counter.json")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	loaded := NewMemStorage()
	if err := loaded.Load(path); err != nil {
		t.Fatalf("Load error = %v", err)
	}

	got, err := loaded.GetCounter("PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if got != bigCounter {
		t.Fatalf("loaded large counter = %d, want %d", got, bigCounter)
	}
}
