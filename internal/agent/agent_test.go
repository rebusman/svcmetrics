package agent

import (
	"fmt"
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
		if got := r.Header.Get("Content-Type"); got != "text/plain" {
			t.Errorf("Content-Type = %q, want text/plain", got)
		}
		if len(body) != 0 {
			t.Errorf("body length = %d, want 0", len(body))
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

	expectedSet := make(map[string]int, len(models.GaugeMetricNames)+len(models.CounterMetricNames))
	for _, name := range models.GaugeMetricNames {
		p := fmt.Sprintf("/update/gauge/%s/%s", name, formatGaugeValue(a.metrics.gauges[name]))
		expectedSet[p]++
	}
	for _, name := range models.CounterMetricNames {
		p := fmt.Sprintf("/update/counter/%s/%d", name, a.metrics.counters[name]-2)
		expectedSet[p]++
	}

	mu.Lock()
	defer mu.Unlock()

	if len(recorded) != len(expectedSet) {
		t.Fatalf("requests count = %d, want %d", len(recorded), len(expectedSet))
	}

	recordedSet := make(map[string]int, len(recorded))
	for _, p := range recorded {
		recordedSet[p]++
	}

	for path, want := range expectedSet {
		if got := recordedSet[path]; got != want {
			t.Fatalf("path %q count = %d, want %d", path, got, want)
		}
	}

	a.mu.RLock()
	if got := a.lastSentCounters["PollCount"]; got != 7 {
		a.mu.RUnlock()
		t.Fatalf("lastSentCounters[PollCount] = %d, want 7", got)
	}
	a.mu.RUnlock()
}
