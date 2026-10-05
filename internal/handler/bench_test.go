package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/rebusman/svcmetrics/internal/hashing"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/repository"
)

// benchBatch builds a batch of n metrics shaped like the agent's: gauges and a
// single counter.
func benchBatch(n int) []models.Metrics {
	batch := make([]models.Metrics, 0, n)
	for i := range n - 1 {
		value := float64(i) * 1.5
		batch = append(batch, models.Metrics{ID: "Gauge" + strconv.Itoa(i), MType: models.Gauge, Value: &value})
	}
	delta := int64(1)
	return append(batch, models.Metrics{ID: models.PollCount, MType: models.Counter, Delta: &delta})
}

// benchBatchBody encodes [benchBatch] as JSON.
func benchBatchBody(b *testing.B, n int) []byte {
	b.Helper()

	body, err := json.Marshal(benchBatch(n))
	if err != nil {
		b.Fatal(err)
	}
	return body
}

// serveBench runs h once on a fresh request and fails on any status but 200.
func serveBench(b *testing.B, h http.Handler, method, target string, body []byte, header http.Header) {
	b.Helper()

	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		b.Fatalf("status = %d, body = %q", w.Code, w.Body.String())
	}
}

func BenchmarkUpdatesJSONHandler(b *testing.B) {
	h := UpdatesJSONHandler(repository.NewMemStorage(), nil)
	body := benchBatchBody(b, 32)

	b.ReportAllocs()
	for b.Loop() {
		serveBench(b, h, http.MethodPost, "/updates/", body, nil)
	}
}

func BenchmarkUpdateJSONHandler(b *testing.B) {
	h := UpdateJSONHandler(repository.NewMemStorage(), nil)
	body := []byte(`{"id":"Alloc","type":"gauge","value":123.5}`)

	b.ReportAllocs()
	for b.Loop() {
		serveBench(b, h, http.MethodPost, "/update", body, nil)
	}
}

func BenchmarkValueJSONHandler(b *testing.B) {
	s := repository.NewMemStorage()
	if _, err := s.UpdateGauge(b.Context(), "Alloc", 123.5); err != nil {
		b.Fatal(err)
	}
	h := ValueJSONHandler(s)
	body := []byte(`{"id":"Alloc","type":"gauge"}`)

	b.ReportAllocs()
	for b.Loop() {
		serveBench(b, h, http.MethodPost, "/value", body, nil)
	}
}

func BenchmarkListHandler(b *testing.B) {
	s := repository.NewMemStorage()
	if err := s.UpdateBatch(b.Context(), benchBatch(32)); err != nil {
		b.Fatal(err)
	}
	h := ListHandler(s)

	b.ReportAllocs()
	for b.Loop() {
		serveBench(b, h, http.MethodGet, "/", nil, nil)
	}
}

// BenchmarkSignedGzipBatch runs a batch through the middleware the router puts
// in front of the handler: signature check, decompression and response
// signing.
func BenchmarkSignedGzipBatch(b *testing.B) {
	const key = "secret"

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(benchBatchBody(b, 32)); err != nil {
		b.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		b.Fatal(err)
	}
	body := gz.Bytes()

	h := HashMiddleware(key)(GzipRequestMiddleware(GzipResponseMiddleware(
		UpdatesJSONHandler(repository.NewMemStorage(), nil))))
	header := http.Header{
		"Content-Type":     {"application/json"},
		"Content-Encoding": {"gzip"},
		"Accept-Encoding":  {"gzip"},
		hashing.Header:     {hashing.Sum(body, key)},
	}

	b.ReportAllocs()
	for b.Loop() {
		serveBench(b, h, http.MethodPost, "/updates/", body, header)
	}
}
