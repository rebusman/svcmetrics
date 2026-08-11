package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rebusman/svcmetrics/internal/hashing"
	models "github.com/rebusman/svcmetrics/internal/model"
)

// readBatch decompresses a request body and decodes the batch it carries.
func readBatch(t *testing.T, r *http.Request) []models.Metrics {
	t.Helper()

	if got := r.Header.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(r.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader error = %v", err)
	}
	defer func() { _ = zr.Close() }()

	var batch []models.Metrics
	if err := json.NewDecoder(zr).Decode(&batch); err != nil {
		t.Fatalf("decode batch error = %v", err)
	}
	return batch
}

func gaugeMetric(name string, value float64) models.Metrics {
	return models.Metrics{ID: name, MType: models.Gauge, Value: &value}
}

func counterMetric(name string, delta int64) models.Metrics {
	return models.Metrics{ID: name, MType: models.Counter, Delta: &delta}
}

// TestSendBatchReusesPooledGzipWriters verifies that the server still receives
// valid gzip after the writer has been recycled through the pool many times: a
// Reset that misses would corrupt the stream.
func TestSendBatchReusesPooledGzipWriters(t *testing.T) {
	const requests = 50

	var (
		mu      sync.Mutex
		decoded [][]models.Metrics
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batch := readBatch(t, r)
		mu.Lock()
		decoded = append(decoded, batch)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(srv.URL, time.Second, time.Second, 0, "")
	for i := range requests {
		if err := a.sendBatch(context.Background(), []models.Metrics{gaugeMetric("Alloc", float64(i)+0.5)}); err != nil {
			t.Fatalf("sendBatch %d error = %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(decoded) != requests {
		t.Fatalf("server received %d bodies, want %d", len(decoded), requests)
	}
	for i, batch := range decoded {
		if len(batch) != 1 {
			t.Fatalf("batch %d has %d metrics, want 1", i, len(batch))
		}
		if batch[0].Value == nil || *batch[0].Value != float64(i)+0.5 {
			t.Errorf("batch %d value = %v, want %v", i, batch[0].Value, float64(i)+0.5)
		}
	}
}

// TestPooledGzipWriterDoesNotRetainRequestBuffer verifies that a writer resting
// in the pool does not point at the buffer it just compressed into, which would
// pin that payload until the next Get.
func TestPooledGzipWriterDoesNotRetainRequestBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(srv.URL, time.Second, time.Second, 0, "")
	if err := a.sendBatch(context.Background(), []models.Metrics{gaugeMetric("Alloc", 12.5)}); err != nil {
		t.Fatalf("sendBatch error = %v", err)
	}

	gw := gzipWriterPool.Get().(*gzip.Writer)
	defer func() {
		gw.Reset(io.Discard)
		gzipWriterPool.Put(gw)
	}()

	dest := reflect.ValueOf(gw).Elem().FieldByName("w")
	if !dest.IsValid() || dest.Kind() != reflect.Interface {
		t.Skip("gzip.Writer has no inspectable destination field anymore")
	}
	if dest.IsNil() {
		return
	}
	if got := dest.Elem().Type().String(); strings.Contains(got, "bytes.Buffer") {
		t.Fatalf("pooled writer still points at %s, retaining the request payload", got)
	}
}

// benchPayload is the body used by the compression benchmarks, which isolate
// what the pool changes: BenchmarkSendBatch is dominated by the HTTP round trip
// and hides the compressor's cost.
var benchPayload = []byte(`[{"id":"Alloc","type":"gauge","value":123456.789}]`)

func BenchmarkGzipCompressPooled(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var buf bytes.Buffer
		gw := gzipWriterPool.Get().(*gzip.Writer)
		gw.Reset(&buf)
		if _, err := gw.Write(benchPayload); err != nil {
			b.Fatal(err)
		}
		if err := gw.Close(); err != nil {
			b.Fatal(err)
		}
		gw.Reset(io.Discard)
		gzipWriterPool.Put(gw)
		_ = buf.Bytes()
	}
}

func BenchmarkGzipCompressFresh(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		if _, err := gw.Write(benchPayload); err != nil {
			b.Fatal(err)
		}
		if err := gw.Close(); err != nil {
			b.Fatal(err)
		}
		_ = buf.Bytes()
	}
}

// BenchmarkSendBatch measures one batch round trip. Run it with -benchmem to
// see the allocation difference the pool buys.
func BenchmarkSendBatch(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(srv.URL, time.Second, time.Second, 0, "")
	batch := []models.Metrics{gaugeMetric("Alloc", 12.5)}

	b.ReportAllocs()
	for b.Loop() {
		if err := a.sendBatch(context.Background(), batch); err != nil {
			b.Fatalf("sendBatch error = %v", err)
		}
	}
}

func TestCollectRuntimeMetrics(t *testing.T) {
	a := New("", 0, 0, 0, "")

	a.CollectRuntimeMetrics()
	a.CollectRuntimeMetrics()

	a.mu.RLock()
	defer a.mu.RUnlock()

	if got := a.metrics.counters["PollCount"]; got != 2 {
		t.Fatalf("PollCount = %d, want 2", got)
	}

	if got := a.metrics.gauges["RandomValue"]; got < 0 || got >= 1 {
		t.Fatalf("RandomValue = %v, want value in [0, 1)", got)
	}

	if got := len(a.metrics.gauges); got != len(models.GaugeMetricNames) {
		t.Fatalf("gauge metrics count = %d, want %d", got, len(models.GaugeMetricNames))
	}
}

// seed fills the agent with one known value per collected metric.
func seed(a *Agent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.metrics.gauges = make(map[string]float64, len(models.GaugeMetricNames))
	for _, name := range models.GaugeMetricNames {
		a.metrics.gauges[name] = 0
	}
	a.metrics.gauges["Alloc"] = 1.5
	a.metrics.gauges["RandomValue"] = 0.75
	a.metrics.counters = map[string]int64{models.PollCount: 7}
	a.lastSentCounters = map[string]int64{models.PollCount: 2}
}

func TestSendMetricsUsesBatches(t *testing.T) {
	const batchSize = 10

	var (
		mu       sync.Mutex
		paths    []string
		received []models.Metrics
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		batch := readBatch(t, r)
		if len(batch) == 0 {
			t.Error("empty batch must never be sent")
		}
		if len(batch) > batchSize {
			t.Errorf("batch of %d metrics exceeds the configured size %d", len(batch), batchSize)
		}

		mu.Lock()
		paths = append(paths, r.URL.Path)
		received = append(received, batch...)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a := New(ts.URL, 2*time.Second, 10*time.Second, batchSize, "")
	a.client = ts.Client()
	seed(a)

	if err := a.SendMetrics(context.Background()); err != nil {
		t.Fatalf("SendMetrics() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	wantMetrics := len(models.GaugeMetricNames) + 1
	wantRequests := (wantMetrics + batchSize - 1) / batchSize

	if len(paths) != wantRequests {
		t.Fatalf("requests count = %d, want %d", len(paths), wantRequests)
	}
	for _, path := range paths {
		if path != "/updates/" {
			t.Errorf("path = %q, want /updates/", path)
		}
	}
	if len(received) != wantMetrics {
		t.Fatalf("metrics sent = %d, want %d", len(received), wantMetrics)
	}

	byID := make(map[string]models.Metrics, len(received))
	for _, m := range received {
		byID[m.ID] = m
	}
	if got := byID["Alloc"]; got.Value == nil || *got.Value != 1.5 {
		t.Errorf("Alloc = %v, want 1.5", got.Value)
	}
	if got := byID[models.PollCount]; got.Delta == nil || *got.Delta != 5 {
		t.Errorf("PollCount delta = %v, want 5", got.Delta)
	}
}

// TestSendMetricsSkipsEmptyBatch verifies that a freshly started agent, which
// has nothing to report, sends no request at all.
func TestSendMetricsSkipsEmptyBatch(t *testing.T) {
	var requests atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a := New(ts.URL, time.Second, time.Second, 0, "")
	a.client = ts.Client()

	if err := a.SendMetrics(context.Background()); err != nil {
		t.Fatalf("SendMetrics() error = %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d requests, want 0", got)
	}
}

// TestSendMetricsReturnsCounterDeltaOnFailure verifies that a report the server
// rejected does not consume the counter delta: the next report carries it
// again.
func TestSendMetricsReturnsCounterDeltaOnFailure(t *testing.T) {
	var (
		mu     sync.Mutex
		deltas []int64
		fail   = true
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batch := readBatch(t, r)

		mu.Lock()
		for _, m := range batch {
			if m.MType == models.Counter && m.Delta != nil {
				deltas = append(deltas, *m.Delta)
			}
		}
		shouldFail := fail
		mu.Unlock()

		if shouldFail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a := New(ts.URL, time.Second, time.Second, 0, "")
	a.client = ts.Client()
	seed(a)

	if err := a.SendMetrics(context.Background()); err == nil {
		t.Fatal("SendMetrics() error = nil, want a failure")
	}

	mu.Lock()
	fail = false
	mu.Unlock()

	if err := a.SendMetrics(context.Background()); err != nil {
		t.Fatalf("SendMetrics() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deltas) != 2 {
		t.Fatalf("counter deltas seen = %v, want two reports", deltas)
	}
	for i, got := range deltas {
		if got != 5 {
			t.Errorf("delta %d = %d, want 5 — a rejected batch must not be lost", i, got)
		}
	}
}

// TestSendBatchesReturnsOnlyUnsentMetrics verifies that only the chunk that
// failed and everything after it is offered for a retry, so whatever the server
// already accepted is never sent twice.
func TestSendBatchesReturnsOnlyUnsentMetrics(t *testing.T) {
	var requests atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	a := New(ts.URL, time.Second, time.Second, 2, "")
	a.client = ts.Client()

	batch := []models.Metrics{
		gaugeMetric("Alloc", 1),
		gaugeMetric("Sys", 2),
		gaugeMetric("HeapSys", 3),
		counterMetric(models.PollCount, 4),
	}

	unsent, err := a.sendBatches(context.Background(), batch)
	if err == nil {
		t.Fatal("sendBatches() error = nil, want a failure")
	}
	if len(unsent) != 2 {
		t.Fatalf("unsent = %d metrics, want the two that were rejected", len(unsent))
	}
	if unsent[0].ID != "HeapSys" || unsent[1].ID != models.PollCount {
		t.Errorf("unsent = %v, want HeapSys and PollCount", unsent)
	}
}

func TestSendBatchSkipsEmptyPayload(t *testing.T) {
	var requests atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a := New(ts.URL, time.Second, time.Second, 0, "")
	a.client = ts.Client()

	if err := a.sendBatch(context.Background(), nil); err != nil {
		t.Fatalf("sendBatch(nil) error = %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d requests, want 0", got)
	}
}

// TestNewBatchSize verifies that New honours the configured batch size and
// falls back to the default only for a non-positive value.
func TestNewBatchSize(t *testing.T) {
	tests := []struct {
		name string
		size int
		want int
	}{
		{name: "configured", size: 5, want: 5},
		{name: "zero falls back", size: 0, want: DefaultBatchSize},
		{name: "negative falls back", size: -3, want: DefaultBatchSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := New("", 0, 0, tt.size, "").batchSize; got != tt.want {
				t.Fatalf("batchSize = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestSendBatchSignsCompressedBody verifies that a keyed agent signs the bytes
// it puts on the wire — the gzip stream — and not the JSON behind it, since the
// server verifies the request before decompressing it.
func TestSendBatchSignsCompressedBody(t *testing.T) {
	const key = "secret"

	var (
		mu   sync.Mutex
		sig  string
		body []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		mu.Lock()
		sig = r.Header.Get(hashing.Header)
		body = raw
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(srv.URL, time.Second, time.Second, 0, key)
	metrics := []models.Metrics{gaugeMetric("Alloc", 1.5)}
	if err := a.sendBatch(context.Background(), metrics); err != nil {
		t.Fatalf("sendBatch() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if sig == "" {
		t.Fatal("a keyed agent sent no signature")
	}
	if !hashing.Equal(sig, body, key) {
		t.Error("signature does not match the transmitted body")
	}

	plain, err := json.Marshal(metrics)
	if err != nil {
		t.Fatalf("marshalling the batch: %v", err)
	}
	if hashing.Equal(sig, plain, key) {
		t.Error("signature covers the uncompressed JSON, want the compressed body")
	}
}

// TestSendBatchWithoutKeyIsUnsigned verifies that an agent without a key sends
// no signature at all, so a server without a key sees an ordinary request.
func TestSendBatchWithoutKeyIsUnsigned(t *testing.T) {
	var (
		mu   sync.Mutex
		seen bool
		sig  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = true
		sig = r.Header.Get(hashing.Header)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(srv.URL, time.Second, time.Second, 0, "")
	if err := a.sendBatch(context.Background(), []models.Metrics{gaugeMetric("Alloc", 1.5)}); err != nil {
		t.Fatalf("sendBatch() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !seen {
		t.Fatal("the request never reached the server")
	}
	if sig != "" {
		t.Errorf("signature = %q, want none", sig)
	}
}
