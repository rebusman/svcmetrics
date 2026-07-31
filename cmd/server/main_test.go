package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// brokenWriter accepts the status line but fails every body write, like a
// client that disconnects mid-response.
type brokenWriter struct {
	hdr    http.Header
	status int
}

func (b *brokenWriter) Header() http.Header {
	if b.hdr == nil {
		b.hdr = make(http.Header)
	}
	return b.hdr
}

func (b *brokenWriter) WriteHeader(code int) { b.status = code }

func (b *brokenWriter) Write([]byte) (int, error) {
	return 0, errors.New("connection reset by peer")
}

func newTestLogger() (*logrus.Logger, *bytes.Buffer) {
	log := logrus.New()
	out := &bytes.Buffer{}
	log.SetOutput(out)
	log.SetLevel(logrus.InfoLevel)
	log.SetFormatter(&logrus.TextFormatter{DisableColors: true, DisableTimestamp: true})
	return log, out
}

// The handlers cannot report a failed body write themselves, so the wrapper
// has to surface it here — and at error level, not buried in the usual
// "Request handled" line.
func TestLoggingMiddlewareReportsBodyWriteFailure(t *testing.T) {
	log, out := newTestLogger()

	h := loggingMiddleware(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"Alloc","type":"gauge","value":12.5}`))
	}))

	rec := &brokenWriter{}
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/value", nil))

	logged := out.String()
	if !strings.Contains(logged, "level=error") {
		t.Errorf("log level is not error:\n%s", logged)
	}
	if !strings.Contains(logged, "Failed to write response body") {
		t.Errorf("log does not name the failure:\n%s", logged)
	}
	if !strings.Contains(logged, "connection reset by peer") {
		t.Errorf("log does not carry the underlying error:\n%s", logged)
	}
	if !strings.Contains(logged, "/value") {
		t.Errorf("log does not identify the request:\n%s", logged)
	}
	if strings.Contains(logged, "Request handled") {
		t.Errorf("a failed request must not also be logged as handled:\n%s", logged)
	}
}

func TestLoggingMiddlewareLogsSuccessfulRequest(t *testing.T) {
	log, out := newTestLogger()

	h := loggingMiddleware(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("12.5"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/value/gauge/Alloc", nil))

	logged := out.String()
	if !strings.Contains(logged, "Request handled") {
		t.Errorf("successful request not logged:\n%s", logged)
	}
	if !strings.Contains(logged, "level=info") {
		t.Errorf("successful request not logged at info level:\n%s", logged)
	}
	if !strings.Contains(logged, "body_size=4") {
		t.Errorf("body size not recorded:\n%s", logged)
	}
	if strings.Contains(logged, "Failed to write response body") {
		t.Errorf("successful request reported a write failure:\n%s", logged)
	}
}

// The status code has to survive the wrapper, otherwise the log would claim
// 200 for every response.
func TestLoggingMiddlewareRecordsStatusCode(t *testing.T) {
	log, out := newTestLogger()

	h := loggingMiddleware(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/value/gauge/Missing", nil))

	if logged := out.String(); !strings.Contains(logged, "status_code=404") {
		t.Errorf("status code not recorded:\n%s", logged)
	}
}
