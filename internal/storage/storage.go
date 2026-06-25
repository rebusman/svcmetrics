package storage

import (
	"errors"
	"sync"
)

type MemStorage struct {
	gaugeMu   sync.RWMutex
	counterMu sync.RWMutex
	gauges    map[string]float64
	counters  map[string]int64
}

func NewMemStorage() *MemStorage {
	return &MemStorage{
		gauges:   make(map[string]float64),
		counters: make(map[string]int64),
	}
}

type Storage interface {
	UpdateGauge(name string, value float64)
	UpdateCounter(name string, value int64)
	GetGauge(name string) (float64, error)
	GetCounter(name string) (int64, error)
}

func (s *MemStorage) UpdateGauge(name string, value float64) {
	s.gaugeMu.Lock()
	defer s.gaugeMu.Unlock()
	s.gauges[name] = value
}

func (s *MemStorage) UpdateCounter(name string, value int64) {
	s.counterMu.Lock()
	defer s.counterMu.Unlock()
	s.counters[name] += value
}

func (s *MemStorage) GetGauge(name string) (float64, error) {
	s.gaugeMu.RLock()
	defer s.gaugeMu.RUnlock()
	val, ok := s.gauges[name]
	if !ok {
		return 0, errors.New("gauge not found")
	}
	return val, nil
}

func (s *MemStorage) GetCounter(name string) (int64, error) {
	s.counterMu.RLock()
	defer s.counterMu.RUnlock()
	val, ok := s.counters[name]
	if !ok {
		return 0, errors.New("counter not found")
	}
	return val, nil
}
