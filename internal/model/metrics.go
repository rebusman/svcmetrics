// Package models defines the metric representation shared by the server, the
// agent and the storages.
package models

import "errors"

// Metric types supported by the service.
const (
	Counter = "counter"
	Gauge   = "gauge"
)

var (
	// ErrNotFound reports that the requested metric is absent from the storage.
	ErrNotFound = errors.New("metric not found")

	// ErrInvalidMetric reports a metric the caller sent wrong: unknown type,
	// missing ID or a value that does not match the type. Storages return it so
	// that handlers can answer 400 instead of 500.
	ErrInvalidMetric = errors.New("invalid metric")
)

// Metric names that the runtime package does not provide.
const (
	PollCount   = "PollCount"
	RandomValue = "RandomValue"
)

// GaugeMetricNames is the single source of truth for the collected gauge metrics.
var GaugeMetricNames = []string{
	"Alloc",
	"BuckHashSys",
	"Frees",
	"GCCPUFraction",
	"GCSys",
	"HeapAlloc",
	"HeapIdle",
	"HeapInuse",
	"HeapObjects",
	"HeapReleased",
	"HeapSys",
	"LastGC",
	"Lookups",
	"MCacheInuse",
	"MCacheSys",
	"MSpanInuse",
	"MSpanSys",
	"Mallocs",
	"NextGC",
	"NumForcedGC",
	"NumGC",
	"OtherSys",
	"PauseTotalNs",
	"StackInuse",
	"StackSys",
	"Sys",
	"TotalAlloc",
	RandomValue,
}

// CounterMetricNames is the single source of truth for the collected counter metrics.
var CounterMetricNames = []string{PollCount}

// Metrics is the flat wire representation of a single metric. Delta and Value
// are pointers so that an unset field stays distinguishable from a stored zero
// and is left out of the JSON encoding.
type Metrics struct {
	ID    string   `json:"id"`
	MType string   `json:"type"`
	Delta *int64   `json:"delta,omitempty"`
	Value *float64 `json:"value,omitempty"`
}
