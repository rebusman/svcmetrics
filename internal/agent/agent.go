// Package agent collects runtime metrics and reports them to the metrics
// server in gzip-compressed batches.
package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/retry"
)

// Defaults applied by New when the caller leaves a setting unset.
const (
	defaultServerAddress  = "http://localhost:8080"
	defaultPollInterval   = 2 * time.Second
	defaultReportInterval = 10 * time.Second
	clientTimeout         = 5 * time.Second

	// DefaultBatchSize is how many metrics go into one POST /updates/ request
	// unless the caller configures something else.
	DefaultBatchSize = 32
)

// gzipWriterPool recycles the compressors used for the request bodies.
var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(nil)
	},
}

// metricState is the set of metrics collected so far.
type metricState struct {
	gauges   map[string]float64
	counters map[string]int64
}

// Agent polls the runtime for metrics and reports them to the server. A report
// that failed for a passing reason is repeated before the metrics it carries
// are handed back to the accumulator. It is safe for concurrent use.
type Agent struct {
	endpoint string
	client   *http.Client

	pollInterval   time.Duration
	reportInterval time.Duration
	batchSize      int

	// retry governs the reports only: collecting metrics from the runtime
	// cannot fail in a way a repetition would fix.
	retry retry.Config

	mu               sync.RWMutex
	metrics          metricState
	lastSentCounters map[string]int64
}

// New returns an agent reporting to endpoint. batchSize caps how many metrics
// travel in one request; a non-positive value of any setting falls back to its
// default.
func New(endpoint string, pollInterval, reportInterval time.Duration, batchSize int) *Agent {
	if endpoint == "" {
		endpoint = defaultServerAddress
	}
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if reportInterval <= 0 {
		reportInterval = defaultReportInterval
	}
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	return &Agent{
		endpoint:       strings.TrimRight(endpoint, "/"),
		client:         &http.Client{Timeout: clientTimeout},
		pollInterval:   pollInterval,
		reportInterval: reportInterval,
		batchSize:      batchSize,
		retry:          retry.Config{Retriable: isRetriableSendError},
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

// reportWithRetry sends the collected metrics, repeating a report that failed
// for a retriable reason on the schedule of [retry.DefaultIntervals]. It stops
// early if the context is cancelled or if the server rejected the batch on its
// merits, which no repetition will change.
//
// The snapshot is taken once and only the batches the server has not accepted
// are retried: resending an accepted batch would count its counter deltas
// twice. Deltas that never made it are handed back for the next report.
func (a *Agent) reportWithRetry(ctx context.Context) {
	pending := a.collectBatch()
	if len(pending) == 0 {
		return
	}

	err := retry.Do(ctx, a.retry, func(ctx context.Context) error {
		var err error
		pending, err = a.sendBatches(ctx, pending)
		return err
	})
	if err != nil {
		a.returnCounters(pending)
	}
}

// CollectRuntimeMetrics reads the runtime memory statistics into the agent's
// state and advances the poll counter.
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

	maps.Copy(a.metrics.gauges, values)
	a.metrics.counters[models.PollCount]++
}

// SendMetrics reports everything collected so far as one or more batches. It
// makes a single attempt: the retrying report is what Run drives.
func (a *Agent) SendMetrics(ctx context.Context) error {
	pending := a.collectBatch()
	if len(pending) == 0 {
		return nil
	}

	unsent, err := a.sendBatches(ctx, pending)
	if err != nil {
		a.returnCounters(unsent)
		return err
	}
	return nil
}

// collectBatch turns the current state into a batch, marking the counters as
// sent. Counters with a zero delta are left out: they carry no information, and
// dropping them keeps an idle agent from sending anything at all. The batch
// order is stable from one report to the next.
func (a *Agent) collectBatch() []models.Metrics {
	gauges, deltas := a.snapshotForReport()

	batch := make([]models.Metrics, 0, len(gauges)+len(deltas))
	for _, name := range models.GaugeMetricNames {
		value, ok := gauges[name]
		if !ok {
			continue
		}
		batch = append(batch, models.Metrics{ID: name, MType: models.Gauge, Value: &value})
	}
	for _, name := range models.CounterMetricNames {
		delta := deltas[name]
		if delta == 0 {
			continue
		}
		batch = append(batch, models.Metrics{ID: name, MType: models.Counter, Delta: &delta})
	}

	return batch
}

// sendBatches ships the metrics in chunks of at most batchSize. On failure it
// returns the metrics that were not accepted, starting with the chunk that
// failed, so the caller can retry exactly those.
func (a *Agent) sendBatches(ctx context.Context, metrics []models.Metrics) ([]models.Metrics, error) {
	for start := 0; start < len(metrics); start += a.batchSize {
		end := min(start+a.batchSize, len(metrics))

		if err := a.sendBatch(ctx, metrics[start:end]); err != nil {
			return metrics[start:], err
		}
	}
	return nil, nil
}

// returnCounters gives the deltas of an unsent batch back to the accumulator so
// that the next report picks them up again.
func (a *Agent) returnCounters(metrics []models.Metrics) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, m := range metrics {
		if m.MType == models.Counter && m.Delta != nil {
			a.lastSentCounters[m.ID] -= *m.Delta
		}
	}
}

// snapshotForReport copies gauges and computes counter deltas under a single lock,
// also marking the counters as sent.
func (a *Agent) snapshotForReport() (map[string]float64, map[string]int64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	gauges := make(map[string]float64, len(a.metrics.gauges))
	maps.Copy(gauges, a.metrics.gauges)

	deltas := make(map[string]int64, len(models.CounterMetricNames))
	for _, name := range models.CounterMetricNames {
		current := a.metrics.counters[name]
		deltas[name] = current - a.lastSentCounters[name]
		a.lastSentCounters[name] = current
	}

	return gauges, deltas
}

// sendBatch posts one gzip-compressed batch to /updates/. An empty batch is
// never sent. The compressor comes from a pool and is pointed back at
// io.Discard before it is returned, so a pooled writer never pins the payload
// it compressed; the response body is drained so the transport can reuse the
// connection for the next batch.
func (a *Agent) sendBatch(ctx context.Context, metrics []models.Metrics) error {
	if len(metrics) == 0 {
		return nil
	}

	body, err := json.Marshal(metrics)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	gw := gzipWriterPool.Get().(*gzip.Writer)
	defer func() {
		gw.Reset(io.Discard)
		gzipWriterPool.Put(gw)
	}()
	gw.Reset(&buf)

	if _, err := gw.Write(body); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	body = buf.Bytes()

	url := fmt.Sprintf("%s/updates/", a.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &statusError{code: resp.StatusCode, status: resp.Status}
	}

	return nil
}
