package storage

import "context"

// Reader describes read access to the metric storage.
type Reader interface {
	GetGauge(ctx context.Context, name string) (float64, error)
	GetCounter(ctx context.Context, name string) (int64, error)
	GetAllGauges(ctx context.Context) (map[string]float64, error)
	GetAllCounters(ctx context.Context) (map[string]int64, error)
}

type Writer interface {
	UpdateGauge(ctx context.Context, name string, value float64) (float64, error)
	UpdateCounter(ctx context.Context, name string, value int64) (int64, error)
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
