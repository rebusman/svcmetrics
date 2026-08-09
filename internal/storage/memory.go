package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	models "github.com/rebusman/svcmetrics/internal/model"
)

type MemStorage struct {
	mu       sync.RWMutex
	gauges   map[string]float64
	counters map[string]int64
}

func NewMemStorage() *MemStorage {
	return &MemStorage{
		gauges:   make(map[string]float64),
		counters: make(map[string]int64),
	}
}

func (s *MemStorage) UpdateGauge(_ context.Context, name string, value float64) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gauges[name] = value
	return value, nil
}

func (s *MemStorage) UpdateCounter(_ context.Context, name string, value int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counters[name] += value
	return s.counters[name], nil
}

func (s *MemStorage) GetGauge(_ context.Context, name string) (float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.gauges[name]
	if !ok {
		return 0, fmt.Errorf("gauge %q: %w", name, models.ErrNotFound)
	}
	return val, nil
}

func (s *MemStorage) GetCounter(_ context.Context, name string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.counters[name]
	if !ok {
		return 0, fmt.Errorf("counter %q: %w", name, models.ErrNotFound)
	}
	return val, nil
}

func (s *MemStorage) GetAllGauges(_ context.Context) (map[string]float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make(map[string]float64, len(s.gauges))
	for k, v := range s.gauges {
		res[k] = v
	}
	return res, nil
}

func (s *MemStorage) GetAllCounters(_ context.Context) (map[string]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make(map[string]int64, len(s.counters))
	for k, v := range s.counters {
		res[k] = v
	}
	return res, nil

}

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

func (s *MemStorage) Save(path string) error {
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

	// CreateTemp makes the file 0600; keep the mode the snapshot used to have.
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

func (s *MemStorage) Load(path string) error {
	data, err := os.ReadFile(path)
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
