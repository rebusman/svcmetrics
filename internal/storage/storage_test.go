package storage

import "testing"

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
