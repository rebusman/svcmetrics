//go:build integration

// The tests in this file need Docker: they run PostgreSQL in a throwaway
// container through testcontainers. Run them with -tags integration.

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// One throwaway Postgres serves the whole package: booting a container per test
// would dominate the runtime. It is started on first use and torn down in
// TestMain, so packages that only run the MemStorage tests never touch Docker.
var (
	pgOnce      sync.Once
	pgContainer *postgres.PostgresContainer
	pgDSN       string
	pgErr       error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgContainer != nil {
		if err := testcontainers.TerminateContainer(pgContainer); err != nil {
			fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
		}
	}
	os.Exit(code)
}

func postgresDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: PgStorage tests need Docker")
	}

	pgOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		pgContainer, pgErr = postgres.Run(ctx, "postgres:16.4-alpine",
			postgres.WithDatabase("metrics"),
			postgres.WithUsername("metrics"),
			postgres.WithPassword("secret"),
			postgres.BasicWaitStrategies(),
		)
		if pgErr != nil {
			return
		}
		pgDSN, pgErr = pgContainer.ConnectionString(ctx, "sslmode=disable")
	})

	if pgErr != nil {
		t.Fatalf("start postgres container: %v", pgErr)
	}
	return pgDSN
}

// newTestPgStorage opens a storage against the shared container. Tests share
// one table, so each one namespaces its metric names with t.Name() instead of
// truncating between runs.
func newTestPgStorage(t *testing.T) *PgStorage {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	s, err := NewPgStorage(ctx, postgresDSN(t))
	if err != nil {
		t.Fatalf("NewPgStorage error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close error = %v", err)
		}
	})
	return s
}

func TestPgStorageAppliesMigrationsAndPings(t *testing.T) {
	s := newTestPgStorage(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.PingContext(ctx); err != nil {
		t.Fatalf("PingContext error = %v", err)
	}

	if _, err := s.UpdateGauge(ctx, t.Name(), 1); err != nil {
		t.Fatalf("UpdateGauge on the migrated schema error = %v", err)
	}
}

// TestPgStorageMigrationsAreIdempotent verifies that a second NewPgStorage
// against the same database does not fail on migrations that are already
// applied, which is what a server restart does.
func TestPgStorageMigrationsAreIdempotent(t *testing.T) {
	first := newTestPgStorage(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	second, err := NewPgStorage(ctx, postgresDSN(t))
	if err != nil {
		t.Fatalf("second NewPgStorage error = %v", err)
	}
	defer second.Close()

	if _, err := first.UpdateGauge(ctx, t.Name(), 2.5); err != nil {
		t.Fatalf("UpdateGauge error = %v", err)
	}
	got, err := second.GetGauge(ctx, t.Name())
	if err != nil {
		t.Fatalf("GetGauge from the second storage error = %v", err)
	}
	if got != 2.5 {
		t.Fatalf("gauge = %v, want 2.5", got)
	}
}

func TestPgStorageGaugeOverwrites(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := t.Name()
	for _, want := range []float64{1.5, -2, 0, 99.25} {
		returned, err := s.UpdateGauge(ctx, name, want)
		if err != nil {
			t.Fatalf("UpdateGauge(%v) error = %v", want, err)
		}
		if returned != want {
			t.Fatalf("UpdateGauge returned %v, want %v", returned, want)
		}
		got, err := s.GetGauge(ctx, name)
		if err != nil {
			t.Fatalf("GetGauge error = %v", err)
		}
		if got != want {
			t.Fatalf("gauge = %v, want %v", got, want)
		}
	}
}

func TestPgStorageCounterAccumulates(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := t.Name()
	var running int64
	for _, step := range []int64{5, 5, 7} {
		running += step
		returned, err := s.UpdateCounter(ctx, name, step)
		if err != nil {
			t.Fatalf("UpdateCounter(%d) error = %v", step, err)
		}
		if returned != running {
			t.Fatalf("UpdateCounter returned %d, want the running total %d", returned, running)
		}
	}

	got, err := s.GetCounter(ctx, name)
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if got != 17 {
		t.Fatalf("counter = %d, want 17 (5+5+7)", got)
	}
}

func TestPgStorageMissingMetricsReportNotFound(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := s.GetGauge(ctx, t.Name()+"-absent"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetGauge error = %v, want ErrNotFound", err)
	}
	if _, err := s.GetCounter(ctx, t.Name()+"-absent"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetCounter error = %v, want ErrNotFound", err)
	}
}

// TestPgStorageSeparatesTypesWithTheSameName verifies that the (id, mtype)
// primary key keeps a gauge and a counter of the same name in two rows.
func TestPgStorageSeparatesTypesWithTheSameName(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := t.Name()
	if _, err := s.UpdateGauge(ctx, name, 4.5); err != nil {
		t.Fatalf("UpdateGauge error = %v", err)
	}
	if _, err := s.GetCounter(ctx, name); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetCounter for a gauge-only name = %v, want ErrNotFound", err)
	}

	if _, err := s.UpdateCounter(ctx, name, 3); err != nil {
		t.Fatalf("UpdateCounter error = %v", err)
	}
	gauge, err := s.GetGauge(ctx, name)
	if err != nil || gauge != 4.5 {
		t.Fatalf("gauge = %v, err = %v, want 4.5, nil", gauge, err)
	}
	counter, err := s.GetCounter(ctx, name)
	if err != nil || counter != 3 {
		t.Fatalf("counter = %d, err = %v, want 3, nil", counter, err)
	}
}

// TestPgStorageGetAll verifies that the readers return only rows of the
// matching type. The table also holds what the other tests wrote, so the test
// checks its own keys.
func TestPgStorageGetAll(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	gaugeName := t.Name() + "-gauge"
	counterName := t.Name() + "-counter"
	if _, err := s.UpdateGauge(ctx, gaugeName, 8.25); err != nil {
		t.Fatalf("UpdateGauge error = %v", err)
	}
	if _, err := s.UpdateCounter(ctx, counterName, 6); err != nil {
		t.Fatalf("UpdateCounter error = %v", err)
	}

	gauges, err := s.GetAllGauges(ctx)
	if err != nil {
		t.Fatalf("GetAllGauges error = %v", err)
	}
	if got, ok := gauges[gaugeName]; !ok || got != 8.25 {
		t.Fatalf("gauges[%q] = %v (present = %v), want 8.25", gaugeName, got, ok)
	}
	if _, ok := gauges[counterName]; ok {
		t.Fatalf("gauges contains counter %q", counterName)
	}

	counters, err := s.GetAllCounters(ctx)
	if err != nil {
		t.Fatalf("GetAllCounters error = %v", err)
	}
	if got, ok := counters[counterName]; !ok || got != 6 {
		t.Fatalf("counters[%q] = %v (present = %v), want 6", counterName, got, ok)
	}
	if _, ok := counters[gaugeName]; ok {
		t.Fatalf("counters contains gauge %q", gaugeName)
	}
}

func TestPgStorageConcurrentCounterUpdatesAreAtomic(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	const writers = 8
	name := t.Name()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		seen    = make(map[int64]int)
		errsGot []error
	)
	for range writers {
		wg.Go(func() {
			got, err := s.UpdateCounter(ctx, name, 1)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errsGot = append(errsGot, err)
				return
			}
			seen[got]++
		})
	}
	wg.Wait()

	if len(errsGot) > 0 {
		t.Fatalf("concurrent UpdateCounter errors = %v", errsGot)
	}

	total, err := s.GetCounter(ctx, name)
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if total != writers {
		t.Fatalf("counter = %d, want %d", total, writers)
	}

	for value := int64(1); value <= writers; value++ {
		if seen[value] != 1 {
			t.Fatalf("total %d was returned %d times, want exactly once (all returned: %v)",
				value, seen[value], seen)
		}
	}
}

// TestPgStorageHonoursContextCancellation verifies that a cancelled request
// context surfaces as an error rather than a zero value.
func TestPgStorageHonoursContextCancellation(t *testing.T) {
	s := newTestPgStorage(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.UpdateGauge(ctx, t.Name(), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateGauge with a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := s.GetGauge(ctx, t.Name()); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetGauge with a cancelled context = %v, want context.Canceled", err)
	}
	if _, err := s.GetAllGauges(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetAllGauges with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestPgStorageFailsAfterClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	s, err := NewPgStorage(ctx, postgresDSN(t))
	if err != nil {
		t.Fatalf("NewPgStorage error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	if err := s.PingContext(ctx); err == nil {
		t.Fatal("PingContext after Close returned nil, want error")
	}
	if _, err := s.UpdateGauge(ctx, t.Name(), 1); err == nil {
		t.Fatal("UpdateGauge after Close returned nil, want error")
	}
}

// TestNewPgStorageRejectsUnreachableDSN does not need Docker: nothing is
// listening on that port.
func TestNewPgStorageRejectsUnreachableDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := NewPgStorage(ctx, "postgres://user:pass@127.0.0.1:1/metrics?sslmode=disable&connect_timeout=2")
	if err == nil {
		s.Close()
		t.Fatal("NewPgStorage with an unreachable DSN returned nil error")
	}
}

func TestNewPgStorageRejectsMalformedDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := NewPgStorage(ctx, "://not-a-dsn")
	if err == nil {
		s.Close()
		t.Fatal("NewPgStorage with a malformed DSN returned nil error")
	}
}

func TestPgStorageUpdateBatch(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	gaugeName := t.Name() + "-gauge"
	counterName := t.Name() + "-counter"

	if _, err := s.UpdateCounter(ctx, counterName, 4); err != nil {
		t.Fatalf("UpdateCounter error = %v", err)
	}

	err := s.UpdateBatch(ctx, []models.Metrics{
		gauge(gaugeName, 1.5),
		counter(counterName, 5),
		gauge(gaugeName, 7.25),
		counter(counterName, 3),
	})
	if err != nil {
		t.Fatalf("UpdateBatch error = %v", err)
	}

	if got, err := s.GetGauge(ctx, gaugeName); err != nil || got != 7.25 {
		t.Errorf("gauge = %v (err %v), want 7.25", got, err)
	}
	if got, err := s.GetCounter(ctx, counterName); err != nil || got != 12 {
		t.Errorf("counter = %v (err %v), want 12 (4+5+3)", got, err)
	}
}

func TestPgStorageUpdateBatchEmpty(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.UpdateBatch(ctx, nil); err != nil {
		t.Fatalf("UpdateBatch(nil) error = %v", err)
	}
}

// TestPgStorageUpdateBatchIsAllOrNothing verifies that a malformed metric
// anywhere in the batch leaves the database untouched.
func TestPgStorageUpdateBatchIsAllOrNothing(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := t.Name()
	err := s.UpdateBatch(ctx, []models.Metrics{
		gauge(name, 1.5),
		{ID: name + "-broken", MType: "histogram"},
	})
	if !errors.Is(err, models.ErrInvalidMetric) {
		t.Fatalf("UpdateBatch error = %v, want ErrInvalidMetric", err)
	}

	if _, err := s.GetGauge(ctx, name); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("gauge error = %v, want ErrNotFound — the batch was rejected", err)
	}
}

// TestPgStorageUpdateBatchRollsBackOnCancelledContext verifies that a cancelled
// context rolls the transaction back rather than leaving half of the batch
// committed.
func TestPgStorageUpdateBatchRollsBackOnCancelledContext(t *testing.T) {
	s := newTestPgStorage(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	name := t.Name()
	if err := s.UpdateBatch(ctx, []models.Metrics{gauge(name, 1.5)}); err == nil {
		t.Fatal("UpdateBatch with a cancelled context returned nil, want error")
	}

	checkCtx, checkCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer checkCancel()
	if _, err := s.GetGauge(checkCtx, name); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("gauge error = %v, want ErrNotFound — the transaction was rolled back", err)
	}
}

// TestPgStorageConcurrentBatchesDoNotDeadlock verifies that batches touching
// the same rows from several connections neither deadlock nor lose increments:
// the rows are ordered identically everywhere.
func TestPgStorageConcurrentBatchesDoNotDeadlock(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const (
		writers = 8
		rounds  = 20
	)
	first := t.Name() + "-a"
	second := t.Name() + "-b"

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range rounds {
				batch := []models.Metrics{counter(first, 1), counter(second, 1)}
				if i%2 == 1 {
					batch[0], batch[1] = batch[1], batch[0]
				}
				if err := s.UpdateBatch(ctx, batch); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
			}
		}(i)
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("concurrent UpdateBatch errors = %v", errs)
	}

	want := int64(writers * rounds)
	for _, name := range []string{first, second} {
		got, err := s.GetCounter(ctx, name)
		if err != nil {
			t.Fatalf("GetCounter(%q) error = %v", name, err)
		}
		if got != want {
			t.Fatalf("counter %q = %d, want %d", name, got, want)
		}
	}
}

// TestPgStorageCounterTreatsNullDeltaAsZero verifies that a counter row left
// with a NULL delta does not poison the accumulation: NULL + delta would be
// NULL and stop the counter from ever growing again.
func TestPgStorageCounterTreatsNullDeltaAsZero(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := t.Name()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO metrics (id, mtype, delta) VALUES ($1, $2, NULL)`, name, models.Counter)
	if err != nil {
		t.Fatalf("seed NULL delta error = %v", err)
	}

	got, err := s.UpdateCounter(ctx, name, 5)
	if err != nil {
		t.Fatalf("UpdateCounter error = %v", err)
	}
	if got != 5 {
		t.Fatalf("UpdateCounter returned %d, want 5", got)
	}

	stored, err := s.GetCounter(ctx, name)
	if err != nil || stored != 5 {
		t.Fatalf("counter = %d (err %v), want 5", stored, err)
	}
}

// TestPgStorageUpdateBatchTreatsNullDeltaAsZero covers the same NULL delta on
// the batch path.
func TestPgStorageUpdateBatchTreatsNullDeltaAsZero(t *testing.T) {
	s := newTestPgStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	name := t.Name()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO metrics (id, mtype, delta) VALUES ($1, $2, NULL)`, name, models.Counter)
	if err != nil {
		t.Fatalf("seed NULL delta error = %v", err)
	}

	if err := s.UpdateBatch(ctx, []models.Metrics{counter(name, 3), counter(name, 4)}); err != nil {
		t.Fatalf("UpdateBatch error = %v", err)
	}

	stored, err := s.GetCounter(ctx, name)
	if err != nil || stored != 7 {
		t.Fatalf("counter = %d (err %v), want 7", stored, err)
	}
}
