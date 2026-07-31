package storage

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

		pgContainer, pgErr = postgres.Run(ctx, "postgres:16-alpine",
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

	// The metrics table exists only if the goose migration ran.
	if _, err := s.UpdateGauge(ctx, t.Name(), 1); err != nil {
		t.Fatalf("UpdateGauge on the migrated schema error = %v", err)
	}
}

// A second NewPgStorage against the same database must not fail on migrations
// that are already applied — that is what a server restart does.
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

// The primary key is (id, mtype), so the same name used as a gauge and as a
// counter must stay two independent rows.
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

// GetAll* must return only rows of the matching type; the table also holds
// whatever the other tests wrote, so this checks its own keys.
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.UpdateCounter(ctx, name, 1)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errsGot = append(errsGot, err)
				return
			}
			seen[got]++
		}()
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

// A cancelled request context must surface as an error rather than a zero value.
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

// Does not need Docker: nothing is listening on that port.
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
