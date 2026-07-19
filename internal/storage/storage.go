package storage

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
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

func (s *MemStorage) UpdateGauge(name string, value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gauges[name] = value
}

func (s *MemStorage) UpdateCounter(name string, value int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counters[name] += value
}

func (s *MemStorage) GetGauge(name string) (float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.gauges[name]
	if !ok {
		return 0, errors.New("gauge not found")
	}
	return val, nil
}

func (s *MemStorage) GetCounter(name string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val, ok := s.counters[name]
	if !ok {
		return 0, errors.New("counter not found")
	}
	return val, nil
}

func (s *MemStorage) GetAllGauges() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make(map[string]float64, len(s.gauges))
	for k, v := range s.gauges {
		res[k] = v
	}
	return res
}

func (s *MemStorage) GetAllCounters() map[string]int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make(map[string]int64, len(s.counters))
	for k, v := range s.counters {
		res[k] = v
	}
	return res
}

func (s *MemStorage) Save(path string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var metrics []interface{}
	for name, val := range s.gauges {
		metrics = append(metrics, map[string]interface{}{
			"id":    name,
			"type":  "gauge",
			"value": val,
		})
	}
	for name, val := range s.counters {
		metrics = append(metrics, map[string]interface{}{
			"id":    name,
			"type":  "counter",
			"delta": val,
		})
	}

	data, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func (s *MemStorage) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var rawMetrics []json.RawMessage
	if err := json.Unmarshal(data, &rawMetrics); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, raw := range rawMetrics {
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}

		id, okID := m["id"].(string)
		mType, okType := m["type"].(string)
		if !okID || !okType {
			continue
		}

		switch mType {
		case "gauge":
			if val, ok := m["value"].(float64); ok {
				s.gauges[id] = val
			}
		case "counter":
			if val, ok := m["delta"].(float64); ok {
				s.counters[id] = int64(val)
			}
		}
	}

	return nil
}
