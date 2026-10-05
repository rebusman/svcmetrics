// Package models defines the metric representation shared by the server, the
// agent and the storages.
package models

import (
	"errors"
	"strconv"
)

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

// Metric names read from the operating system rather than from the Go runtime.
const (
	TotalMemory = "TotalMemory"
	FreeMemory  = "FreeMemory"
)

// CPUUtilization returns the gauge name carrying the load of the n-th CPU,
// numbered from one. How many of these names exist is not known until run
// time, because it follows the number of CPUs the host reports.
func CPUUtilization(n int) string {
	return "CPUutilization" + strconv.Itoa(n)
}

// RuntimeGaugeMetricNames is the single source of truth for the gauge metrics
// read from the Go runtime.
var RuntimeGaugeMetricNames = []string{
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

// SystemGaugeMetricNames is the single source of truth for the gauge metrics
// read from the host that are known ahead of time. The CPUutilization gauges
// are deliberately absent: their names are built by [CPUUtilization] once the
// number of CPUs is known.
var SystemGaugeMetricNames = []string{
	TotalMemory,
	FreeMemory,
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
