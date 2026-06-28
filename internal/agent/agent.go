package agent

import (
	"fmt"
	"math/rand"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultServerAddress  = "http://localhost:8080"
	defaultPollInterval   = 2
	defaultReportInterval = 10
)

var gaugeMetricNames = []string{
	"Alloc",
	"BuckHashSys",
	"Frees",
	"GCCPUFraction",
	"GCSys",
	"HeapAlloc",
	"HeapIdle",
	"HeapInuse",
	"HeapObjects",
	"HeapReleased",
	"HeapSys",
	"LastGC",
	"Lookups",
	"MCacheInuse",
	"MCacheSys",
	"MSpanInuse",
	"MSpanSys",
	"Mallocs",
	"NextGC",
	"NumForcedGC",
	"NumGC",
	"OtherSys",
	"PauseTotalNs",
	"StackInuse",
	"StackSys",
	"Sys",
	"TotalAlloc",
	"RandomValue",
}

var counterMetricNames = []string{"PollCount"}

type metricState struct {
	gauges   map[string]float64
	counters map[string]int64
}

type Agent struct {
	endpoint string
	client   *http.Client
	rng      *rand.Rand

	pollIntervalSec   int
	reportIntervalSec int

	mu               sync.RWMutex
	metrics          metricState
	lastSentCounters map[string]int64
}

func New(endpoint string, pollIntervalSec, reportIntervalSec int) *Agent {
	if endpoint == "" {
		endpoint = defaultServerAddress
	}
	if pollIntervalSec <= 0 {
		pollIntervalSec = defaultPollInterval
	}
	if reportIntervalSec <= 0 {
		reportIntervalSec = defaultReportInterval
	}

	return &Agent{
		endpoint:          strings.TrimRight(endpoint, "/"),
		client:            &http.Client{Timeout: 5 * time.Second},
		rng:               rand.New(rand.NewSource(time.Now().UnixNano())),
		pollIntervalSec:   pollIntervalSec,
		reportIntervalSec: reportIntervalSec,
		metrics: metricState{
			gauges:   make(map[string]float64, len(gaugeMetricNames)),
			counters: make(map[string]int64, len(counterMetricNames)),
		},
		lastSentCounters: make(map[string]int64, len(counterMetricNames)),
	}
}

func (a *Agent) Run() {
	go func() {
		for {
			time.Sleep(time.Duration(a.pollIntervalSec) * time.Second)
			a.CollectRuntimeMetrics()
		}
	}()

	for {
		time.Sleep(time.Duration(a.reportIntervalSec) * time.Second)
		_ = a.SendMetrics()
	}
}

func (a *Agent) CollectRuntimeMetrics() {
	a.ensureInitialized()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	a.mu.Lock()
	defer a.mu.Unlock()

	a.metrics.gauges["Alloc"] = float64(ms.Alloc)
	a.metrics.gauges["BuckHashSys"] = float64(ms.BuckHashSys)
	a.metrics.gauges["Frees"] = float64(ms.Frees)
	a.metrics.gauges["GCCPUFraction"] = ms.GCCPUFraction
	a.metrics.gauges["GCSys"] = float64(ms.GCSys)
	a.metrics.gauges["HeapAlloc"] = float64(ms.HeapAlloc)
	a.metrics.gauges["HeapIdle"] = float64(ms.HeapIdle)
	a.metrics.gauges["HeapInuse"] = float64(ms.HeapInuse)
	a.metrics.gauges["HeapObjects"] = float64(ms.HeapObjects)
	a.metrics.gauges["HeapReleased"] = float64(ms.HeapReleased)
	a.metrics.gauges["HeapSys"] = float64(ms.HeapSys)
	a.metrics.gauges["LastGC"] = float64(ms.LastGC)
	a.metrics.gauges["Lookups"] = float64(ms.Lookups)
	a.metrics.gauges["MCacheInuse"] = float64(ms.MCacheInuse)
	a.metrics.gauges["MCacheSys"] = float64(ms.MCacheSys)
	a.metrics.gauges["MSpanInuse"] = float64(ms.MSpanInuse)
	a.metrics.gauges["MSpanSys"] = float64(ms.MSpanSys)
	a.metrics.gauges["Mallocs"] = float64(ms.Mallocs)
	a.metrics.gauges["NextGC"] = float64(ms.NextGC)
	a.metrics.gauges["NumForcedGC"] = float64(ms.NumForcedGC)
	a.metrics.gauges["NumGC"] = float64(ms.NumGC)
	a.metrics.gauges["OtherSys"] = float64(ms.OtherSys)
	a.metrics.gauges["PauseTotalNs"] = float64(ms.PauseTotalNs)
	a.metrics.gauges["StackInuse"] = float64(ms.StackInuse)
	a.metrics.gauges["StackSys"] = float64(ms.StackSys)
	a.metrics.gauges["Sys"] = float64(ms.Sys)
	a.metrics.gauges["TotalAlloc"] = float64(ms.TotalAlloc)
	a.metrics.gauges["RandomValue"] = a.rng.Float64()

	a.metrics.counters["PollCount"]++
}

func (a *Agent) SendMetrics() error {
	a.ensureInitialized()

	gauges, counters := a.snapshotMetrics()

	for _, name := range gaugeMetricNames {
		if err := a.sendMetric("gauge", name, formatGaugeValue(gauges[name])); err != nil {
			return err
		}
	}

	for _, name := range counterMetricNames {
		current := counters[name]
		previous := a.lastSentCounter(name)
		delta := current - previous
		if err := a.sendMetric("counter", name, strconv.FormatInt(delta, 10)); err != nil {
			return err
		}
		a.setLastSentCounter(name, current)
	}

	return nil
}

func (a *Agent) snapshotMetrics() (map[string]float64, map[string]int64) {
	a.ensureInitialized()

	a.mu.RLock()
	defer a.mu.RUnlock()

	gauges := make(map[string]float64, len(a.metrics.gauges))
	for k, v := range a.metrics.gauges {
		gauges[k] = v
	}

	counters := make(map[string]int64, len(a.metrics.counters))
	for k, v := range a.metrics.counters {
		counters[k] = v
	}

	return gauges, counters
}

func (a *Agent) lastSentCounter(name string) int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastSentCounters[name]
}

func (a *Agent) setLastSentCounter(name string, value int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastSentCounters[name] = value
}

func (a *Agent) sendMetric(metricType, name, value string) error {
	url := fmt.Sprintf("%s/update/%s/%s/%s", a.endpoint, metricType, name, value)
	req, err := http.NewRequest(http.MethodPost, url, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")

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

func (a *Agent) ensureInitialized() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client == nil {
		a.client = &http.Client{Timeout: 5 * time.Second}
	}
	if a.rng == nil {
		a.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if a.metrics.gauges == nil {
		a.metrics.gauges = make(map[string]float64, len(gaugeMetricNames))
	}
	if a.metrics.counters == nil {
		a.metrics.counters = make(map[string]int64, len(counterMetricNames))
	}
	if a.lastSentCounters == nil {
		a.lastSentCounters = make(map[string]int64, len(counterMetricNames))
	}
}
