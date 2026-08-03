//go:build e2e

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/repository"
	"github.com/sirupsen/logrus"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var sharedDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("metrics"),
		postgres.WithUsername("metrics"),
		postgres.WithPassword("metrics"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {

		log.Printf("e2e: cannot start postgres container (is Docker running?): %v", err)
		os.Exit(m.Run())
	}

	sharedDSN, err = pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = pgContainer.Terminate(ctx)
		log.Fatalf("e2e: failed to build connection string: %v", err)
	}

	code := m.Run()

	if err := pgContainer.Terminate(ctx); err != nil {
		log.Printf("e2e: failed to terminate postgres container: %v", err)
	}

	os.Exit(code)
}

func newDB(t *testing.T) *sql.DB {
	t.Helper()

	if sharedDSN == "" {
		t.Skip("skipping e2e test: postgres container is not available (is Docker running?)")
	}

	db, err := sql.Open("pgx", sharedDSN)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return db
}

func newTestServer(t *testing.T, pinger interface {
	PingContext(context.Context) error
}) *httptest.Server {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	r := newRouter(log, repository.NewMemStorage(), pinger)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// newPgStorage builds a PgStorage against the shared container, applying the
// migrations, and clears the metrics table so tests do not see each other's rows.
func newPgStorage(t *testing.T) *repository.PgStorage {
	t.Helper()

	if sharedDSN == "" {
		t.Skip("skipping e2e test: postgres container is not available (is Docker running?)")
	}

	ctx := context.Background()
	pg, err := repository.NewPgStorage(ctx, sharedDSN)
	if err != nil {
		t.Fatalf("failed to create pg storage: %v", err)
	}
	t.Cleanup(func() { pg.Close() })

	db := newDB(t)
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE metrics"); err != nil {
		t.Fatalf("failed to truncate metrics: %v", err)
	}

	return pg
}

func TestE2EPostgresStorageRoundTrip(t *testing.T) {
	pg := newPgStorage(t)

	log := logrus.New()
	log.SetOutput(io.Discard)
	srv := httptest.NewServer(newRouter(log, pg, pg))
	t.Cleanup(srv.Close)

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Post(srv.URL+"/update/gauge/Alloc/3.14", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST /update/gauge failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /update/gauge status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	delta := int64(10)
	body, err := json.Marshal(models.Metrics{ID: "PollCount", MType: models.Counter, Delta: &delta})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	for i := range 2 {
		resp, err := client.Post(srv.URL+"/update", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /update failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("POST /update status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		var echoed models.Metrics
		decodeErr := json.NewDecoder(resp.Body).Decode(&echoed)
		resp.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode /update response: %v", decodeErr)
		}
		if want := delta * int64(i+1); echoed.Delta == nil || *echoed.Delta != want {
			t.Fatalf("POST /update returned delta = %v, want %d", echoed.Delta, want)
		}
	}

	fresh, err := repository.NewPgStorage(context.Background(), sharedDSN)
	if err != nil {
		t.Fatalf("failed to reopen pg storage: %v", err)
	}
	defer fresh.Close()

	gauge, err := fresh.GetGauge(context.Background(), "Alloc")
	if err != nil {
		t.Fatalf("GetGauge error = %v", err)
	}
	if gauge != 3.14 {
		t.Fatalf("gauge Alloc = %v, want 3.14", gauge)
	}

	counter, err := fresh.GetCounter(context.Background(), "PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if counter != 20 {
		t.Fatalf("counter PollCount = %d, want 20", counter)
	}

	t.Logf("e2e OK: metrics persisted in PostgreSQL (Alloc=%v, PollCount=%d)", gauge, counter)
}

func TestE2EPostgresStorageNotFound(t *testing.T) {
	pg := newPgStorage(t)

	if _, err := pg.GetGauge(context.Background(), "DoesNotExist"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetGauge error = %v, want models.ErrNotFound", err)
	}
	if _, err := pg.GetCounter(context.Background(), "DoesNotExist"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("GetCounter error = %v, want models.ErrNotFound", err)
	}

	t.Log("e2e OK: PgStorage reports models.ErrNotFound for unknown metrics")
}

func TestE2EPingHealthy(t *testing.T) {
	db := newDB(t)
	srv := newTestServer(t, db)

	resp, err := http.Get(srv.URL + "/ping")
	if err != nil {
		t.Fatalf("GET /ping failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ping status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	t.Log("e2e OK: GET /ping returned 200 with a healthy database")
}

func TestE2EPingUnhealthy(t *testing.T) {
	db := newDB(t)
	srv := newTestServer(t, db)

	if err := db.Close(); err != nil {
		t.Fatalf("failed to close db: %v", err)
	}

	resp, err := http.Get(srv.URL + "/ping")
	if err != nil {
		t.Fatalf("GET /ping failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("GET /ping status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}

	t.Log("e2e OK: GET /ping returned 500 after the database connection was closed")
}

func TestE2EMetricsRoundTrip(t *testing.T) {
	db := newDB(t)
	srv := newTestServer(t, db)

	client := &http.Client{Timeout: 5 * time.Second}

	delta := int64(10)
	body, err := json.Marshal(models.Metrics{ID: "PollCount", MType: models.Counter, Delta: &delta})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	for i := range 2 {
		resp, err := client.Post(srv.URL+"/update", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST /update failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /update status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		t.Logf("e2e cycle %d/2: POST /update accepted (delta=%d)", i+1, delta)
	}

	resp, err := client.Get(srv.URL + "/value/counter/PollCount")
	if err != nil {
		t.Fatalf("GET /value failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /value status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != "20" {
		t.Fatalf("counter value = %q, want %q", string(got), "20")
	}

	t.Logf("e2e OK: counter accumulated correctly over 2 cycles, GET /value returned %q", string(got))
}

func TestE2EUpdatePlainGauge(t *testing.T) {
	db := newDB(t)
	srv := newTestServer(t, db)

	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Post(srv.URL+"/update/gauge/Alloc/3.14", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST /update/gauge failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /update/gauge status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	t.Log("e2e OK: POST /update/gauge/Alloc/3.14 accepted")

	resp, err = client.Get(srv.URL + "/value/gauge/Alloc")
	if err != nil {
		t.Fatalf("GET /value/gauge failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /value/gauge status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != "3.14" {
		t.Fatalf("gauge value = %q, want %q", string(got), "3.14")
	}

	t.Logf("e2e OK: GET /value/gauge/Alloc returned %q", string(got))
}

func TestE2EValueJSON(t *testing.T) {
	db := newDB(t)
	srv := newTestServer(t, db)

	client := &http.Client{Timeout: 5 * time.Second}

	value := 42.5
	body, err := json.Marshal(models.Metrics{ID: "HeapAlloc", MType: models.Gauge, Value: &value})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := client.Post(srv.URL+"/update", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /update failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /update status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	query, err := json.Marshal(models.Metrics{ID: "HeapAlloc", MType: models.Gauge})
	if err != nil {
		t.Fatalf("marshal query: %v", err)
	}

	resp, err = client.Post(srv.URL+"/value", "application/json", bytes.NewReader(query))
	if err != nil {
		t.Fatalf("POST /value failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /value status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result models.Metrics
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Value == nil {
		t.Fatalf("response Value is nil, want %v", value)
	}
	if *result.Value != value {
		t.Fatalf("response Value = %v, want %v", *result.Value, value)
	}

	t.Logf("e2e OK: POST /value returned gauge HeapAlloc = %v", *result.Value)
}

func TestE2EValueJSONNotFound(t *testing.T) {
	db := newDB(t)
	srv := newTestServer(t, db)

	client := &http.Client{Timeout: 5 * time.Second}

	query, err := json.Marshal(models.Metrics{ID: "DoesNotExist", MType: models.Gauge})
	if err != nil {
		t.Fatalf("marshal query: %v", err)
	}

	resp, err := client.Post(srv.URL+"/value", "application/json", bytes.NewReader(query))
	if err != nil {
		t.Fatalf("POST /value failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /value status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	t.Log("e2e OK: POST /value returned 404 for an unknown metric")
}

func TestE2EListHTML(t *testing.T) {
	db := newDB(t)
	srv := newTestServer(t, db)

	client := &http.Client{Timeout: 5 * time.Second}

	body, err := json.Marshal(models.Metrics{ID: "Sys", MType: models.Gauge, Value: float64Ptr(7)})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := client.Post(srv.URL+"/update", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /update failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /update status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	page, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Contains(page, []byte("Sys")) {
		t.Fatalf("index page does not list metric %q; body: %s", "Sys", page)
	}

	t.Log("e2e OK: GET / rendered the stored metric name")
}

// TestE2EConcurrentCounterUpdatesArePersistedExactly verifies that concurrent
// counter updates go through a single INSERT ... ON CONFLICT ... RETURNING, so
// every request observes its own running total and no increment is lost.
func TestE2EConcurrentCounterUpdatesArePersistedExactly(t *testing.T) {
	pg := newPgStorage(t)

	log := logrus.New()
	log.SetOutput(io.Discard)
	srv := httptest.NewServer(newRouter(log, pg, pg))
	t.Cleanup(srv.Close)

	const writers = 16
	client := &http.Client{Timeout: 10 * time.Second}

	delta := int64(1)
	body, err := json.Marshal(models.Metrics{ID: "PollCount", MType: models.Counter, Delta: &delta})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		totals   = make(map[int64]int)
		failures []error
	)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			resp, err := client.Post(srv.URL+"/update", "application/json", bytes.NewReader(body))
			if err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
				return
			}
			defer resp.Body.Close()

			var echoed models.Metrics
			if err := json.NewDecoder(resp.Body).Decode(&echoed); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if echoed.Delta == nil {
				failures = append(failures, errors.New("response carried no delta"))
				return
			}
			totals[*echoed.Delta]++
		}()
	}
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("concurrent updates failed: %v", failures)
	}

	stored, err := pg.GetCounter(context.Background(), "PollCount")
	if err != nil {
		t.Fatalf("GetCounter error = %v", err)
	}
	if stored != writers {
		t.Fatalf("counter in the database = %d, want %d", stored, writers)
	}
	for want := int64(1); want <= writers; want++ {
		if totals[want] != 1 {
			t.Fatalf("total %d returned %d times, want exactly once (all: %v)", want, totals[want], totals)
		}
	}

	t.Logf("e2e OK: %d concurrent updates each saw a distinct total, database holds %d", writers, stored)
}

// TestE2EGzipRoundTripAgainstPostgres exercises the compressed request path
// against a real server and a real database: the agent always reports gzipped.
func TestE2EGzipRoundTripAgainstPostgres(t *testing.T) {
	pg := newPgStorage(t)

	log := logrus.New()
	log.SetOutput(io.Discard)
	srv := httptest.NewServer(newRouter(log, pg, pg))
	t.Cleanup(srv.Close)

	client := &http.Client{Timeout: 5 * time.Second}

	value := 42.5
	raw, err := json.Marshal(models.Metrics{ID: "HeapAlloc", MType: models.Gauge, Value: &value})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/update", bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /update failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /update status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}

	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader on the response: %v", err)
	}
	defer zr.Close()

	var echoed models.Metrics
	if err := json.NewDecoder(zr).Decode(&echoed); err != nil {
		t.Fatalf("decode gzipped response: %v", err)
	}
	if echoed.Value == nil || *echoed.Value != value {
		t.Fatalf("response value = %v, want %v", echoed.Value, value)
	}

	stored, err := pg.GetGauge(context.Background(), "HeapAlloc")
	if err != nil {
		t.Fatalf("GetGauge error = %v", err)
	}
	if stored != value {
		t.Fatalf("stored gauge = %v, want %v", stored, value)
	}

	t.Logf("e2e OK: gzipped request and response round-tripped through PostgreSQL (%v)", stored)
}

// TestE2EPingWithoutDatabase verifies that without a database configured the
// router still serves metrics and /ping is the only endpoint that reports the
// missing dependency.
func TestE2EPingWithoutDatabase(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	srv := httptest.NewServer(newRouter(log, repository.NewMemStorage(), nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/ping")
	if err != nil {
		t.Fatalf("GET /ping failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("GET /ping status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}

	resp2, err := http.Post(srv.URL+"/update/gauge/Alloc/1.5", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST /update failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("POST /update status = %d, want %d — metrics must work without a database", resp2.StatusCode, http.StatusOK)
	}

	t.Log("e2e OK: /ping reports 500 without a database while metrics keep working")
}

func float64Ptr(v float64) *float64 { return &v }

// TestE2EGzippedBatchAgainstPostgres exercises the batch endpoint through the
// whole stack: gzipped request, one transaction, PostgreSQL behind it.
func TestE2EGzippedBatchAgainstPostgres(t *testing.T) {
	pg := newPgStorage(t)

	log := logrus.New()
	log.SetOutput(io.Discard)
	srv := httptest.NewServer(newRouter(log, pg, pg))
	t.Cleanup(srv.Close)

	client := &http.Client{Timeout: 5 * time.Second}

	alloc, heap := 1.5, 42.25
	delta := int64(7)
	extra := int64(3)
	raw, err := json.Marshal([]models.Metrics{
		{ID: "Alloc", MType: models.Gauge, Value: &alloc},
		{ID: "HeapAlloc", MType: models.Gauge, Value: &heap},
		{ID: "PollCount", MType: models.Counter, Delta: &delta},
		{ID: "PollCount", MType: models.Counter, Delta: &extra},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/updates/", bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /updates/ failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /updates/ status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	ctx := context.Background()
	if got, err := pg.GetGauge(ctx, "Alloc"); err != nil || got != alloc {
		t.Errorf("Alloc = %v (err %v), want %v", got, err, alloc)
	}
	if got, err := pg.GetGauge(ctx, "HeapAlloc"); err != nil || got != heap {
		t.Errorf("HeapAlloc = %v (err %v), want %v", got, err, heap)
	}
	if got, err := pg.GetCounter(ctx, "PollCount"); err != nil || got != delta+extra {
		t.Errorf("PollCount = %v (err %v), want %v", got, err, delta+extra)
	}

	bad, err := json.Marshal([]models.Metrics{
		{ID: "Sys", MType: models.Gauge, Value: &alloc},
		{ID: "Broken", MType: "histogram"},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	badResp, err := client.Post(srv.URL+"/updates/", "application/json", bytes.NewReader(bad))
	if err != nil {
		t.Fatalf("POST /updates/ failed: %v", err)
	}
	defer badResp.Body.Close()

	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed batch status = %d, want %d", badResp.StatusCode, http.StatusBadRequest)
	}
	if _, err := pg.GetGauge(ctx, "Sys"); !errors.Is(err, models.ErrNotFound) {
		t.Fatalf("Sys error = %v, want ErrNotFound — the batch was rejected as a whole", err)
	}

	t.Logf("e2e OK: gzipped batch committed in one transaction against PostgreSQL")
}
