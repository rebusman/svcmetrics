// Package repository holds the metric backends: an in-memory one with optional
// file persistence and a PostgreSQL one.
package repository

import (
	"context"

	models "github.com/rebusman/svcmetrics/internal/model"
)

//go:generate go tool mockgen -destination=../mocks/repository.go -package=mocks github.com/rebusman/svcmetrics/internal/repository Storage,Reader,Writer

// Reader describes read access to the metric storage. Gauges and counters live
// in separate namespaces: the same name may exist as both.
type Reader interface {
	// GetGauge returns the current value of the gauge called name, or an
	// error wrapping [models.ErrNotFound] if there is none.
	GetGauge(ctx context.Context, name string) (float64, error)

	// GetCounter returns the running total of the counter called name, or an
	// error wrapping [models.ErrNotFound] if there is none.
	GetCounter(ctx context.Context, name string) (int64, error)

	// GetAllGauges returns every stored gauge by name. The map belongs to the
	// caller: changing it does not affect the storage.
	GetAllGauges(ctx context.Context) (map[string]float64, error)

	// GetAllCounters returns every stored counter by name. The map belongs to
	// the caller: changing it does not affect the storage.
	GetAllCounters(ctx context.Context) (map[string]int64, error)
}

// Writer describes write access to the metric storage.
type Writer interface {
	// UpdateGauge replaces the value of the gauge called name, creating it if
	// needed, and returns the value now stored.
	UpdateGauge(ctx context.Context, name string, value float64) (float64, error)

	// UpdateCounter adds value to the counter called name, starting it from
	// zero if needed, and returns the new running total.
	UpdateCounter(ctx context.Context, name string, value int64) (int64, error)

	// UpdateBatch applies a whole batch atomically: either every metric in it
	// becomes visible to readers, or none does. Within a batch a gauge keeps
	// its last value and counter deltas add up. A batch with a metric missing
	// its ID, of an unknown type or without the value its type requires is
	// rejected in full with an error wrapping [models.ErrInvalidMetric]; an
	// empty batch is a no-op.
	UpdateBatch(ctx context.Context, metrics []models.Metrics) error
}

// Storage is the contract every metric backend implements. It deliberately
// leaves out PingContext and Close: only [PgStorage] owns a connection, so those
// live on the concrete type and are consumed through
// [github.com/rebusman/svcmetrics/internal/handler.Pinger].
type Storage interface {
	Reader
	Writer
}

var (
	_ Storage = (*MemStorage)(nil)
	_ Storage = (*PgStorage)(nil)
)
