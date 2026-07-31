package agent

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	models "github.com/rebusman/svcmetrics/internal/model"
)

// The server must still receive valid gzip after the writer has been recycled
// through the pool many times — a Reset that misses would corrupt the stream.
func TestSendMetricReusesPooledGzipWriters(t *testing.T) {
	const requests = 50

	var (
		mu      sync.Mutex
		decoded []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Encoding"); got != "gzip" {
			t.Errorf("Content-Encoding = %q, want gzip", got)
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip.NewReader error = %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { _ = zr.Close() }()

		body, err := io.ReadAll(zr)
		if err != nil {
			t.Errorf("read decompressed body error = %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		decoded = append(decoded, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(srv.URL, time.Second, time.Second)
	for i := range requests {
		if err := a.sendMetric(models.Gauge, "Alloc", strconv.Itoa(i)+".5"); err != nil {
			t.Fatalf("sendMetric %d error = %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(decoded) != requests {
		t.Fatalf("server received %d bodies, want %d", len(decoded), requests)
	}
	for i, body := range decoded {
		want := `"value":` + strconv.Itoa(i) + `.5`
		if !strings.Contains(body, want) {
			t.Errorf("body %d = %s, want it to contain %s", i, body, want)
		}
	}
}

// These two isolate what the pool changes: BenchmarkSendMetric below is
// dominated by the HTTP round trip, which hides the compressor's cost.
var benchPayload = []byte(`{"id":"Alloc","type":"gauge","value":123456.789}`)

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

// Run with -benchmem to see the allocation difference the pool buys.
func BenchmarkSendMetric(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(srv.URL, time.Second, time.Second)

	b.ReportAllocs()
	for b.Loop() {
		if err := a.sendMetric(models.Gauge, "Alloc", "12.5"); err != nil {
			b.Fatalf("sendMetric error = %v", err)
		}
	}
}

func TestCollectRuntimeMetrics(t *testing.T) {
	a := New("", 0, 0)

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

func TestSendMetrics(t *testing.T) {
	var (
		mu       sync.Mutex
		recorded []string
		statuses = make(map[string]int)
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		_ = r.Body.Close()

		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if len(body) == 0 {
			t.Errorf("body should not be empty")
		}

		mu.Lock()
		recorded = append(recorded, r.URL.Path)
		statuses[r.URL.Path]++
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a := New(ts.URL, 2*time.Second, 10*time.Second)
	a.client = ts.Client()

	a.mu.Lock()
	a.metrics.gauges = make(map[string]float64, len(models.GaugeMetricNames))
	for _, name := range models.GaugeMetricNames {
		a.metrics.gauges[name] = 0
	}
	a.metrics.gauges["Alloc"] = 1.5
	a.metrics.gauges["RandomValue"] = 0.75
	a.metrics.counters = map[string]int64{"PollCount": 7}
	a.lastSentCounters = map[string]int64{"PollCount": 2}
	a.mu.Unlock()

	if err := a.SendMetrics(); err != nil {
		t.Fatalf("SendMetrics() error = %v", err)
	}

	expectedCount := len(models.GaugeMetricNames) + len(models.CounterMetricNames)

	mu.Lock()
	defer mu.Unlock()

	if len(recorded) != expectedCount {
		t.Fatalf("requests count = %d, want %d", len(recorded), expectedCount)
	}

	for _, path := range recorded {
		if path != "/update" {
			t.Errorf("path = %q, want /update", path)
		}
	}
}

func TestSendMetricInvalidValue(t *testing.T) {
	a := New("http://example.com", 0, 0)

	tests := []struct {
		name       string
		metricType string
		value      string
	}{
		{name: "gauge", metricType: models.Gauge, value: "not-a-number"},
		{name: "counter", metricType: models.Counter, value: "not-an-int"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := a.sendMetric(tt.metricType, "metric", tt.value); err == nil {
				t.Fatalf("sendMetric() error = nil, want error")
			}
		})
	}
}

// An unknown type must fail before anything is sent, rather than being
// serialized as a counter and rejected by the server.
func TestSendMetricRejectsUnknownType(t *testing.T) {
	var requests atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := New(srv.URL, time.Second, time.Second)

	tests := []struct {
		name       string
		metricType string
		value      string
	}{
		{name: "unknown type", metricType: "histogram", value: "1"},
		{name: "empty type", metricType: "", value: "1"},
		{name: "wrong case", metricType: "Gauge", value: "1.5"},
		{name: "value parseable as int", metricType: "summary", value: "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := a.sendMetric(tt.metricType, "SomeMetric", tt.value)
			if err == nil {
				t.Fatalf("sendMetric(%q) error = nil, want error", tt.metricType)
			}
			// The message has to name the offending type, otherwise the caller
			// cannot tell which metric was misconfigured.
			if !strings.Contains(err.Error(), tt.metricType) && tt.metricType != "" {
				t.Errorf("error %q does not mention the type %q", err, tt.metricType)
			}
			if !strings.Contains(err.Error(), "SomeMetric") {
				t.Errorf("error %q does not mention the metric name", err)
			}
		})
	}

	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d requests, want 0 — nothing may be sent for an unsupported type", got)
	}
}
