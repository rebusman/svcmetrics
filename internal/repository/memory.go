package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"

	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/retry"
)

// MemStorage keeps metrics in memory and can persist them to a JSON file.
// Every method is safe for concurrent use.
type MemStorage struct {
	mu       sync.RWMutex
	gauges   map[string]float64
	counters map[string]int64

	// retry governs the snapshot file operations only: the in-memory ones
	// cannot fail in a way a repetition would fix.
	retry retry.Config
}

// NewMemStorage returns an empty in-memory storage.
func NewMemStorage() *MemStorage {
	return &MemStorage{
		gauges:   make(map[string]float64),
		counters: make(map[string]int64),
		retry:    retry.Config{Retriable: isRetriableFileError},
	}
}

// UpdateGauge stores value under name and returns it.
func (s *MemStorage) UpdateGauge(_ context.Context, name string, value float64) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gauges[name] = value
	return value, nil
}

// UpdateCounter adds value to the counter and returns the running total.
func (s *MemStorage) UpdateCounter(_ context.Context, name string, value int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counters[name] += value
	return s.counters[name], nil
}

// UpdateBatch applies the batch under a single lock, so a reader never observes
// half of it. The batch is validated before the lock is taken: a malformed one
// leaves the storage untouched and returns models.ErrInvalidMetric.
func (s *MemStorage) UpdateBatch(_ context.Context, metrics []models.Metrics) error {
	if len(metrics) == 0 {
		return nil
	}

	batch, err := aggregateBatch(metrics)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, m := range batch {
		switch m.MType {
		case models.Gauge:
			s.gauges[m.ID] = *m.Value
		case models.Counter:
			s.counters[m.ID] += *m.Delta
		}
	}
	return nil
}

// GetGauge returns the stored gauge, or models.ErrNotFound if it is absent.
func (s *MemStorage) GetGauge(_ context.Context, name string) (float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.gauges[name]
	if !ok {
		return 0, fmt.Errorf("gauge %q: %w", name, models.ErrNotFound)
	}
	return val, nil
}

// GetCounter returns the stored counter, or models.ErrNotFound if it is absent.
func (s *MemStorage) GetCounter(_ context.Context, name string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.counters[name]
	if !ok {
		return 0, fmt.Errorf("counter %q: %w", name, models.ErrNotFound)
	}
	return val, nil
}

// GetAllGauges returns a copy of every stored gauge.
func (s *MemStorage) GetAllGauges(_ context.Context) (map[string]float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make(map[string]float64, len(s.gauges))
	maps.Copy(res, s.gauges)
	return res, nil
}

// GetAllCounters returns a copy of every stored counter.
func (s *MemStorage) GetAllCounters(_ context.Context) (map[string]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make(map[string]int64, len(s.counters))
	maps.Copy(res, s.counters)
	return res, nil

}

// snapshot copies the current state into a flat metric slice.
func (s *MemStorage) snapshot() []models.Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()

	metrics := make([]models.Metrics, 0, len(s.gauges)+len(s.counters))
	for name, val := range s.gauges {
		v := val
		metrics = append(metrics, models.Metrics{ID: name, MType: models.Gauge, Value: &v})
	}
	for name, val := range s.counters {
		d := val
		metrics = append(metrics, models.Metrics{ID: name, MType: models.Counter, Delta: &d})
	}
	return metrics
}

// Save writes a snapshot to path atomically: the data lands in a temporary
// file next to the target, which then replaces it by a rename. A write that
// fails for a passing reason — the target held open by another process, a busy
// device — is repeated; each attempt starts from its own temporary file, so a
// half-written one is never promoted.
func (s *MemStorage) Save(path string) error {
	return retry.Do(context.Background(), s.retry, func(context.Context) error {
		return s.save(path)
	})
}

// save writes one snapshot to path.
func (s *MemStorage) save(path string) error {
	metrics := s.snapshot()

	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if err := writeAndClose(tmp, data); err != nil {
		os.Remove(tmpName)
		return err
	}

	if err := os.Chmod(tmpName, 0644); err != nil {
		os.Remove(tmpName)
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// writeAndClose writes data to f, flushes it to disk and closes f. The sync
// matters: without it a rename can land before the contents do.
func writeAndClose(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load restores metrics from a snapshot written by Save, merging them into
// whatever the storage already holds. A read that fails for a passing reason is
// repeated; a missing or malformed snapshot is reported at once.
func (s *MemStorage) Load(path string) error {
	data, err := retry.DoValue(context.Background(), s.retry, func(context.Context) ([]byte, error) {
		return os.ReadFile(path)
	})
	if err != nil {
		return err
	}

	var metrics []models.Metrics
	if err := json.Unmarshal(data, &metrics); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, m := range metrics {
		switch m.MType {
		case models.Gauge:
			if m.Value != nil {
				s.gauges[m.ID] = *m.Value
			}
		case models.Counter:
			if m.Delta != nil {
				s.counters[m.ID] = *m.Delta
			}
		}
	}

	return nil
}
