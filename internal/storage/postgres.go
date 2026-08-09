package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/migrations"
)

type PgStorage struct {
	db *sql.DB
}

func NewPgStorage(ctx context.Context, dsn string) (*PgStorage, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := migrations.Up(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	return &PgStorage{db: db}, nil
}

func (s *PgStorage) UpdateGauge(ctx context.Context, name string, value float64) (float64, error) {
	var stored float64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO metrics (id, mtype, value)
		VALUES ($1, $2, $3)
		ON CONFLICT (id, mtype) DO UPDATE SET value = EXCLUDED.value
		RETURNING value`,
		name, models.Gauge, value).Scan(&stored)
	if err != nil {
		return 0, fmt.Errorf("update gauge %q: %w", name, err)
	}
	return stored, nil
}

func (s *PgStorage) UpdateCounter(ctx context.Context, name string, value int64) (int64, error) {
	var stored int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO metrics (id, mtype, delta)
		VALUES ($1, $2, $3)
		ON CONFLICT (id, mtype) DO UPDATE SET delta = metrics.delta + EXCLUDED.delta
		RETURNING delta`,
		name, models.Counter, value).Scan(&stored)
	if err != nil {
		return 0, fmt.Errorf("update counter %q: %w", name, err)
	}
	return stored, nil
}

func (s *PgStorage) GetGauge(ctx context.Context, name string) (float64, error) {
	var value sql.NullFloat64
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM metrics WHERE id = $1 AND mtype = $2`,
		name, models.Gauge).Scan(&value)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("gauge %q: %w", name, models.ErrNotFound)
	case err != nil:
		return 0, fmt.Errorf("get gauge %q: %w", name, err)
	case !value.Valid:
		return 0, fmt.Errorf("gauge %q: %w", name, models.ErrNotFound)
	}
	return value.Float64, nil
}

func (s *PgStorage) GetCounter(ctx context.Context, name string) (int64, error) {
	var delta sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT delta FROM metrics WHERE id = $1 AND mtype = $2`,
		name, models.Counter).Scan(&delta)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("counter %q: %w", name, models.ErrNotFound)
	case err != nil:
		return 0, fmt.Errorf("get counter %q: %w", name, err)
	case !delta.Valid:
		return 0, fmt.Errorf("counter %q: %w", name, models.ErrNotFound)
	}
	return delta.Int64, nil
}

func (s *PgStorage) GetAllGauges(ctx context.Context) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, value FROM metrics WHERE mtype = $1 AND value IS NOT NULL`, models.Gauge)
	if err != nil {
		return nil, fmt.Errorf("get all gauges: %w", err)
	}
	defer rows.Close()

	res := make(map[string]float64)
	for rows.Next() {
		var (
			name  string
			value float64
		)
		if err := rows.Scan(&name, &value); err != nil {
			return nil, fmt.Errorf("scan gauge: %w", err)
		}
		res[name] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get all gauges: %w", err)
	}
	return res, nil
}

func (s *PgStorage) GetAllCounters(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, delta FROM metrics WHERE mtype = $1 AND delta IS NOT NULL`, models.Counter)
	if err != nil {
		return nil, fmt.Errorf("get all counters: %w", err)
	}
	defer rows.Close()

	res := make(map[string]int64)
	for rows.Next() {
		var (
			name  string
			delta int64
		)
		if err := rows.Scan(&name, &delta); err != nil {
			return nil, fmt.Errorf("scan counter: %w", err)
		}
		res[name] = delta
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get all counters: %w", err)
	}
	return res, nil
}

func (s *PgStorage) PingContext(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *PgStorage) Close() error {
	return s.db.Close()
}
