package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	models "github.com/rebusman/svcmetrics/internal/model"
)

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
