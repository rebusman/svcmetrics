package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	models "github.com/rebusman/svcmetrics/internal/model"
)

const (
	defaultServerAddress  = "http://localhost:8080"
	defaultPollInterval   = 2 * time.Second
	defaultReportInterval = 10 * time.Second
	clientTimeout         = 5 * time.Second

	reportMaxAttempts   = 3
	reportRetryInterval = 1 * time.Second
)

var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(nil)
	},
}

type metricState struct {
	gauges   map[string]float64
	counters map[string]int64
}

type Agent struct {
	endpoint string
	client   *http.Client

	pollInterval   time.Duration
	reportInterval time.Duration

	mu               sync.RWMutex
	metrics          metricState
	lastSentCounters map[string]int64
}

func New(endpoint string, pollInterval, reportInterval time.Duration) *Agent {
	if endpoint == "" {
		endpoint = defaultServerAddress
	}
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if reportInterval <= 0 {
		reportInterval = defaultReportInterval
	}

	return &Agent{
		endpoint:       strings.TrimRight(endpoint, "/"),
		client:         &http.Client{Timeout: clientTimeout},
		pollInterval:   pollInterval,
		reportInterval: reportInterval,
		metrics: metricState{
			gauges:   make(map[string]float64, len(models.GaugeMetricNames)),
			counters: make(map[string]int64, len(models.CounterMetricNames)),
		},
		lastSentCounters: make(map[string]int64, len(models.CounterMetricNames)),
	}
}

// Run collects and reports metrics until the context is cancelled.
func (a *Agent) Run(ctx context.Context) {
	pollTicker := time.NewTicker(a.pollInterval)
	defer pollTicker.Stop()

	reportTicker := time.NewTicker(a.reportInterval)
	defer reportTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-pollTicker.C:
			a.CollectRuntimeMetrics()
		case <-reportTicker.C:
			a.reportWithRetry(ctx)
		}
	}
}

// reportWithRetry sends metrics, retrying on failure up to reportMaxAttempts
// times with a fixed backoff. It stops early if the context is cancelled.
func (a *Agent) reportWithRetry(ctx context.Context) {
	var err error
	for attempt := 1; attempt <= reportMaxAttempts; attempt++ {
		if err = a.SendMetrics(); err == nil {
			return
		}

		if attempt == reportMaxAttempts {
			break
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(reportRetryInterval):
		}
	}

	// All attempts failed; keep the agent running and try again on the next tick.
	_ = err
}

func (a *Agent) CollectRuntimeMetrics() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	values := map[string]float64{
		"Alloc":            float64(ms.Alloc),
		"BuckHashSys":      float64(ms.BuckHashSys),
		"Frees":            float64(ms.Frees),
		"GCCPUFraction":    ms.GCCPUFraction,
		"GCSys":            float64(ms.GCSys),
		"HeapAlloc":        float64(ms.HeapAlloc),
		"HeapIdle":         float64(ms.HeapIdle),
		"HeapInuse":        float64(ms.HeapInuse),
		"HeapObjects":      float64(ms.HeapObjects),
		"HeapReleased":     float64(ms.HeapReleased),
		"HeapSys":          float64(ms.HeapSys),
		"LastGC":           float64(ms.LastGC),
		"Lookups":          float64(ms.Lookups),
		"MCacheInuse":      float64(ms.MCacheInuse),
		"MCacheSys":        float64(ms.MCacheSys),
		"MSpanInuse":       float64(ms.MSpanInuse),
		"MSpanSys":         float64(ms.MSpanSys),
		"Mallocs":          float64(ms.Mallocs),
		"NextGC":           float64(ms.NextGC),
		"NumForcedGC":      float64(ms.NumForcedGC),
		"NumGC":            float64(ms.NumGC),
		"OtherSys":         float64(ms.OtherSys),
		"PauseTotalNs":     float64(ms.PauseTotalNs),
		"StackInuse":       float64(ms.StackInuse),
		"StackSys":         float64(ms.StackSys),
		"Sys":              float64(ms.Sys),
		"TotalAlloc":       float64(ms.TotalAlloc),
		models.RandomValue: rand.Float64(),
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for name, value := range values {
		a.metrics.gauges[name] = value
	}
	a.metrics.counters[models.PollCount]++
}

func (a *Agent) SendMetrics() error {
	gauges, counterDeltas := a.snapshotForReport()

	var g errgroup.Group

	for _, name := range models.GaugeMetricNames {
		name := name
		g.Go(func() error {
			return a.sendMetric(models.Gauge, name, formatGaugeValue(gauges[name]))
		})
	}

	for _, name := range models.CounterMetricNames {
		name := name
		g.Go(func() error {
			return a.sendMetric(models.Counter, name, strconv.FormatInt(counterDeltas[name], 10))
		})
	}

	return g.Wait()
}

// snapshotForReport copies gauges and computes counter deltas under a single lock,
// also marking the counters as sent.
func (a *Agent) snapshotForReport() (map[string]float64, map[string]int64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	gauges := make(map[string]float64, len(a.metrics.gauges))
	for k, v := range a.metrics.gauges {
		gauges[k] = v
	}

	deltas := make(map[string]int64, len(models.CounterMetricNames))
	for _, name := range models.CounterMetricNames {
		current := a.metrics.counters[name]
		deltas[name] = current - a.lastSentCounters[name]
		a.lastSentCounters[name] = current
	}

	return gauges, deltas
}

func (a *Agent) sendMetric(metricType, name, value string) error {
	m := models.Metrics{
		ID:    name,
		MType: metricType,
	}

	// Only the two known types are accepted: falling through to the counter
	// branch would silently ship a mistyped metric that the server then
	// rejects, with nothing pointing back at the caller.
	switch metricType {
	case models.Gauge:
		val, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("parse gauge value %q: %w", value, err)
		}
		m.Value = &val
	case models.Counter:
		val, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("parse counter value %q: %w", value, err)
		}
		m.Delta = &val
	default:
		return fmt.Errorf("unsupported metric type %q for metric %q", metricType, name)
	}

	body, err := json.Marshal(m)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	gw := gzipWriterPool.Get().(*gzip.Writer)
	defer func() {
		// Point the writer away from this request's buffer before pooling it:
		// a writer sitting in the pool would otherwise keep the payload (and
		// its backing array) alive until the next Get. Reset also restores a
		// writer left in a bad state by a failed compression.
		gw.Reset(io.Discard)
		gzipWriterPool.Put(gw)
	}()
	gw.Reset(&buf)

	if _, err := gw.Write(body); err != nil {
		return err
	}
	// Close flushes the gzip trailer; the buffer is only complete afterwards.
	if err := gw.Close(); err != nil {
		return err
	}
	body = buf.Bytes()

	url := fmt.Sprintf("%s/update", a.endpoint)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	return nil
}

func formatGaugeValue(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
