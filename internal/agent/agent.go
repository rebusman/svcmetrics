// Package agent collects metrics and reports them to the metrics server in
// gzip-compressed batches. An agent configured with a key signs the compressed
// request body and puts the digest in [hashing.Header]; an agent without one
// sends no signature at all.
//
// Collecting and reporting run apart: two collectors — one reading the Go
// runtime, one reading the host through gopsutil — write into the shared
// state, while a reporter cuts that state into batches and hands them to a
// pool of workers that do the sending. The pool is the rate limit: however
// much there is to report, no more than one request per worker is ever in
// flight.
package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rebusman/svcmetrics/internal/hashing"
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

	// DefaultRateLimit is how many reports may be in flight at once unless the
	// caller configures something else. One keeps an agent that has fallen
	// behind from turning into a burst of requests the server has to absorb.
	DefaultRateLimit = 1
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

// Agent polls the runtime and the host for metrics and reports them to the
// server. A report that failed for a passing reason is repeated before the
// metrics it carries are handed back to the accumulator. It is safe for
// concurrent use.
type Agent struct {
	// updatesURL is the batch endpoint of the server, built once rather than
	// on every report.
	updatesURL string
	client     *http.Client

	pollInterval   time.Duration
	reportInterval time.Duration
	batchSize      int
	rateLimit      int
	key            string

	// retry governs the reports only: reading the runtime cannot fail in a way
	// a repetition would fix, and a host statistic that could not be read is
	// simply left to the next poll.
	retry retry.Config

	// onError, when non-nil, sees the failures the agent survives on its own:
	// a host statistic it could not read and a report it gave up on.
	onError func(error)

	mu               sync.RWMutex
	metrics          metricState
	lastSentCounters map[string]int64

	// cpuCount is how many CPUutilization gauges the previous host reading left
	// in the state, so that a reading finding fewer CPUs can drop the ones that
	// went away. It is guarded by mu.
	cpuCount int
}

// New returns an agent reporting to endpoint. batchSize caps how many metrics
// travel in one request and rateLimit how many requests are in flight at once;
// a non-positive value of any setting falls back to its default. A non-empty
// key enables HMAC-SHA256 request signing.
func New(endpoint string, pollInterval, reportInterval time.Duration, batchSize int, key string, rateLimit int) *Agent {
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
	if rateLimit <= 0 {
		rateLimit = DefaultRateLimit
	}

	return &Agent{
		updatesURL:     strings.TrimRight(endpoint, "/") + "/updates/",
		client:         &http.Client{Timeout: clientTimeout},
		pollInterval:   pollInterval,
		reportInterval: reportInterval,
		batchSize:      batchSize,
		rateLimit:      rateLimit,
		key:            key,
		retry:          retry.Config{Retriable: isRetriableSendError},
		metrics: metricState{
			gauges:   make(map[string]float64, gaugeCapacity()),
			counters: make(map[string]int64, len(models.CounterMetricNames)),
		},
		lastSentCounters: make(map[string]int64, len(models.CounterMetricNames)),
	}
}

// SetOnRetry installs fn as the observer of the repeated reports, so the
// caller can log the failures the agent recovers from.
func (a *Agent) SetOnRetry(fn retry.OnRetry) {
	a.retry.OnRetry = fn
}

// SetOnError installs fn as the observer of the failures the agent survives on
// its own: a host statistic it could not read and a report it gave up on after
// every repetition failed. Without it those failures pass silently.
func (a *Agent) SetOnError(fn func(error)) {
	a.onError = fn
}

// reportError hands err to the observer, if there is one.
func (a *Agent) reportError(err error) {
	if a.onError != nil {
		a.onError(err)
	}
}

// Run collects and reports metrics until the context is cancelled, and returns
// once every goroutine it started has stopped.
//
// The collectors, the reporter and the workers are separate goroutines joined
// by a single channel of batches. Run drives the reporter itself and blocks,
// as it did when all of this happened in one loop.
//
// The workers and the collectors stop on different signals — the workers when
// the channel of batches closes, the collectors when the context is cancelled —
// and neither has to outlive the other. A single [sync.WaitGroup] holds them
// all, so the two kinds are joined at once rather than one pool after the
// other, and nothing a worker does delays a collector's completion.
func (a *Agent) Run(ctx context.Context) {
	batches := make(chan []models.Metrics)

	var running sync.WaitGroup
	for range a.rateLimit {
		running.Go(func() {
			a.work(ctx, batches)
		})
	}
	running.Go(func() {
		a.poll(ctx, a.collectRuntime)
	})
	running.Go(func() {
		a.poll(ctx, a.CollectSystemMetrics)
	})

	a.report(ctx, batches)

	// Only the reporter sends, so it is the one that may close the channel;
	// the workers then finish what they are already holding and stop.
	close(batches)
	running.Wait()
}

// collectRuntime adapts [Agent.CollectRuntimeMetrics] to the signature poll
// expects, and always returns nil.
//
// The collector itself keeps the signature it deserves: reading the runtime
// statistics cannot fail, and an exported method returning an error that is
// always nil would be worse than this adapter.
func (a *Agent) collectRuntime(context.Context) error {
	a.CollectRuntimeMetrics()
	return nil
}

// poll runs collect on every tick of the poll interval until the context is
// cancelled, handing the failures to the observer. A failed collection is not
// repeated: the next tick is another attempt, and it comes sooner than a
// repetition would.
func (a *Agent) poll(ctx context.Context, collect func(context.Context) error) {
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := collect(ctx); err != nil {
				a.reportError(err)
			}
		}
	}
}

// report cuts the collected metrics into batches on every tick of the report
// interval and hands them to the workers until the context is cancelled.
func (a *Agent) report(ctx context.Context, batches chan<- []models.Metrics) {
	ticker := time.NewTicker(a.reportInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !a.enqueue(ctx, batches) {
				return
			}
		}
	}
}

// enqueue splits the metrics collected so far into batches of at most
// batchSize and offers them to the workers, waiting while they are all busy —
// that back pressure is what holds the number of requests in flight down to
// the rate limit. It returns false once the context is cancelled, handing the
// deltas of the batches nobody took back to the accumulator.
func (a *Agent) enqueue(ctx context.Context, batches chan<- []models.Metrics) bool {
	pending := a.collectBatch()

	for start := 0; start < len(pending); start += a.batchSize {
		end := min(start+a.batchSize, len(pending))

		select {
		case <-ctx.Done():
			a.returnCounters(pending[start:])
			return false
		case batches <- pending[start:end]:
		}
	}
	return true
}

// work sends the batches the reporter produces, one at a time, until the
// channel is closed. Each worker is one permitted request in flight, which is
// what makes the size of the pool the rate limit.
func (a *Agent) work(ctx context.Context, batches <-chan []models.Metrics) {
	for batch := range batches {
		a.sendWithRetry(ctx, batch)
	}
}

// sendWithRetry sends one batch, repeating a report that failed for a
// retriable reason on the schedule of [retry.DefaultIntervals]. It stops early
// if the context is cancelled or if the server rejected the batch on its
// merits, which no repetition will change; the deltas of a batch that never
// arrived are handed back for the next report.
//
// Retrying inside the worker is what keeps a struggling server from costing
// more than one slot in the pool: the batch waits on the worker holding it
// instead of on a request of its own.
func (a *Agent) sendWithRetry(ctx context.Context, batch []models.Metrics) {
	err := retry.Do(ctx, a.retry, func(ctx context.Context) error {
		return a.sendBatch(ctx, batch)
	})
	if err != nil {
		a.returnCounters(batch)
		a.reportError(err)
	}
}

// gaugeCapacity estimates how many gauges an agent ends up holding, so that
// the map behind them is allocated once.
func gaugeCapacity() int {
	return len(models.RuntimeGaugeMetricNames) + len(models.SystemGaugeMetricNames) + runtime.NumCPU()
}

// CollectRuntimeMetrics reads the runtime memory statistics into the agent's
// state and advances the poll counter. The values go straight into the state:
// an intermediate map would be allocated and thrown away on every poll.
func (a *Agent) CollectRuntimeMetrics() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	random := rand.Float64()

	a.mu.Lock()
	defer a.mu.Unlock()

	g := a.metrics.gauges
	g["Alloc"] = float64(ms.Alloc)
	g["BuckHashSys"] = float64(ms.BuckHashSys)
	g["Frees"] = float64(ms.Frees)
	g["GCCPUFraction"] = ms.GCCPUFraction
	g["GCSys"] = float64(ms.GCSys)
	g["HeapAlloc"] = float64(ms.HeapAlloc)
	g["HeapIdle"] = float64(ms.HeapIdle)
	g["HeapInuse"] = float64(ms.HeapInuse)
	g["HeapObjects"] = float64(ms.HeapObjects)
	g["HeapReleased"] = float64(ms.HeapReleased)
	g["HeapSys"] = float64(ms.HeapSys)
	g["LastGC"] = float64(ms.LastGC)
	g["Lookups"] = float64(ms.Lookups)
	g["MCacheInuse"] = float64(ms.MCacheInuse)
	g["MCacheSys"] = float64(ms.MCacheSys)
	g["MSpanInuse"] = float64(ms.MSpanInuse)
	g["MSpanSys"] = float64(ms.MSpanSys)
	g["Mallocs"] = float64(ms.Mallocs)
	g["NextGC"] = float64(ms.NextGC)
	g["NumForcedGC"] = float64(ms.NumForcedGC)
	g["NumGC"] = float64(ms.NumGC)
	g["OtherSys"] = float64(ms.OtherSys)
	g["PauseTotalNs"] = float64(ms.PauseTotalNs)
	g["StackInuse"] = float64(ms.StackInuse)
	g["StackSys"] = float64(ms.StackSys)
	g["Sys"] = float64(ms.Sys)
	g["TotalAlloc"] = float64(ms.TotalAlloc)
	g[models.RandomValue] = random

	a.metrics.counters[models.PollCount]++
}

// SendMetrics reports everything collected so far as one or more batches, sent
// one after another. It makes a single attempt and does not go through the
// worker pool — the retrying, rate-limited report is what Run drives — so it
// stays within the limit by never having more than one request in flight.
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
// dropping them keeps an idle agent from sending anything at all.
//
// The gauges are ordered by name rather than by a fixed list, because the
// CPUutilization gauges are not known until the host has been read; sorting
// still keeps the batch order stable from one report to the next.
//
// The batch is built under the lock directly from the state, with the values
// its metrics point at held in one slice per kind: no copy of the state and no
// allocation per metric.
func (a *Agent) collectBatch() []models.Metrics {
	a.mu.Lock()

	gauges := make([]float64, 0, len(a.metrics.gauges))
	deltas := make([]int64, 0, len(models.CounterMetricNames))
	batch := make([]models.Metrics, 0, cap(gauges)+cap(deltas))

	for name, value := range a.metrics.gauges {
		gauges = append(gauges, value)
		batch = append(batch, models.Metrics{ID: name, MType: models.Gauge, Value: &gauges[len(gauges)-1]})
	}
	gaugeCount := len(batch)

	for _, name := range models.CounterMetricNames {
		current := a.metrics.counters[name]
		delta := current - a.lastSentCounters[name]
		a.lastSentCounters[name] = current
		if delta == 0 {
			continue
		}
		deltas = append(deltas, delta)
		batch = append(batch, models.Metrics{ID: name, MType: models.Counter, Delta: &deltas[len(deltas)-1]})
	}

	a.mu.Unlock()

	slices.SortFunc(batch[:gaugeCount], func(x, y models.Metrics) int {
		return strings.Compare(x.ID, y.ID)
	})
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

// sendBatch posts one gzip-compressed batch to /updates/. When the agent has a
// key, the complete compressed body is signed and the digest goes into
// [hashing.Header] — the server verifies the request before it decompresses
// it, so the JSON behind the gzip is the wrong thing to sign. An empty batch is
// never sent. The
// compressor comes from a pool and is pointed back at io.Discard before it is
// returned, so a pooled writer never pins the payload it compressed; the
// response body is drained so the transport can reuse the connection for the
// next batch.
func (a *Agent) sendBatch(ctx context.Context, metrics []models.Metrics) error {
	if len(metrics) == 0 {
		return nil
	}

	var buf bytes.Buffer
	gw := gzipWriterPool.Get().(*gzip.Writer)
	defer func() {
		gw.Reset(io.Discard)
		gzipWriterPool.Put(gw)
	}()
	gw.Reset(&buf)

	// The JSON is encoded straight into the compressor, so the uncompressed
	// body never exists as a whole.
	if err := json.NewEncoder(gw).Encode(metrics); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	body := buf.Bytes()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.updatesURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	// The response body is thrown away unread, so a compressed one would only
	// cost the server a compressor and the agent a decompressor.
	req.Header.Set("Accept-Encoding", "identity")
	if a.key != "" {
		req.Header.Set(hashing.Header, hashing.Sum(body, a.key))
	}

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
