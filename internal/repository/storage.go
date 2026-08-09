// Package repository holds the metric backends: an in-memory one with optional
// file persistence and a PostgreSQL one.
package repository

import (
	"context"

	models "github.com/rebusman/svcmetrics/internal/model"
)

//go:generate go tool mockgen -destination=../mocks/repository.go -package=mocks github.com/rebusman/svcmetrics/internal/repository Storage,Reader,Writer

// Reader describes read access to the metric storage.
type Reader interface {
	GetGauge(ctx context.Context, name string) (float64, error)
	GetCounter(ctx context.Context, name string) (int64, error)
	GetAllGauges(ctx context.Context) (map[string]float64, error)
	GetAllCounters(ctx context.Context) (map[string]int64, error)
}

// Writer describes write access to the metric storage. UpdateBatch applies a
// whole batch atomically: either every metric in it becomes visible to readers,
// or none does.
type Writer interface {
	UpdateGauge(ctx context.Context, name string, value float64) (float64, error)
	UpdateCounter(ctx context.Context, name string, value int64) (int64, error)
	UpdateBatch(ctx context.Context, metrics []models.Metrics) error
}

// Storage is the contract every metric backend implements. It deliberately
// leaves out PingContext and Close: only PgStorage owns a connection, so those
// live on the concrete type and are consumed through handler.Pinger.
type Storage interface {
	Reader
	Writer
}

var (
	_ Storage = (*MemStorage)(nil)
	_ Storage = (*PgStorage)(nil)
)
