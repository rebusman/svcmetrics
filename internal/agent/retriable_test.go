package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	models "github.com/rebusman/svcmetrics/internal/model"
)

func TestIsRetriableSendError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "server error",
			err:  &statusError{code: http.StatusInternalServerError, status: "500 Internal Server Error"},
			want: true,
		},
		{
			name: "service unavailable",
			err:  &statusError{code: http.StatusServiceUnavailable, status: "503 Service Unavailable"},
			want: true,
		},
		{
			name: "too many requests",
			err:  &statusError{code: http.StatusTooManyRequests, status: "429 Too Many Requests"},
			want: true,
		},
		{
			name: "bad request",
			err:  &statusError{code: http.StatusBadRequest, status: "400 Bad Request"},
			want: false,
		},
		{
			name: "not found",
			err:  &statusError{code: http.StatusNotFound, status: "404 Not Found"},
			want: false,
		},
		{
			name: "wrapped server error",
			err:  fmt.Errorf("send batch: %w", &statusError{code: http.StatusBadGateway, status: "502 Bad Gateway"}),
			want: true,
		},
		{
			name: "connection refused",
			err:  &url.Error{Op: "Post", URL: "http://localhost:8080/updates/", Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}},
			want: true,
		},
		{
			name: "client timeout",
			err:  &url.Error{Op: "Post", URL: "http://localhost:8080/updates/", Err: context.DeadlineExceeded},
			want: true,
		},
		{
			name: "connection reset mid-response",
			err:  io.ErrUnexpectedEOF,
			want: true,
		},
		{
			name: "unencodable payload",
			err:  &json.UnsupportedTypeError{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetriableSendError(tt.err); got != tt.want {
				t.Fatalf("isRetriableSendError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// newTestAgent returns an agent talking to ts and retrying without waiting.
func newTestAgent(ts *httptest.Server) *Agent {
	a := New(ts.URL, time.Second, time.Second, 0)
	a.client = ts.Client()
	a.retry.Intervals = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	return a
}

// TestReportRetriesUntilServerRecovers verifies that a server which is briefly
// unavailable does not cost the report: the batch is offered again and the
// counter delta stays consumed once it lands.
func TestReportRetriesUntilServerRecovers(t *testing.T) {
	var requests atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a := newTestAgent(ts)
	seed(a)

	a.reportWithRetry(context.Background())

	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3 — the report must be repeated", got)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if got := a.lastSentCounters[models.PollCount]; got != 7 {
		t.Fatalf("lastSentCounters[PollCount] = %d, want 7 — an accepted delta must not be resent", got)
	}
}

// TestReportDoesNotRetryRejectedBatch verifies that a batch the server refused
// on its merits is offered exactly once, and that its counter delta is handed
// back so the next report carries it again.
func TestReportDoesNotRetryRejectedBatch(t *testing.T) {
	var requests atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	a := newTestAgent(ts)
	seed(a)

	a.reportWithRetry(context.Background())

	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 — a rejected batch must not be repeated", got)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if got := a.lastSentCounters[models.PollCount]; got != 2 {
		t.Fatalf("lastSentCounters[PollCount] = %d, want 2 — an unsent delta must be returned", got)
	}
}

// TestReportGivesUpAfterTheSchedule verifies that a server that never recovers
// costs one attempt per interval plus the original one, and that nothing is
// lost: the delta goes back to the accumulator.
func TestReportGivesUpAfterTheSchedule(t *testing.T) {
	var requests atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	a := newTestAgent(ts)
	seed(a)

	a.reportWithRetry(context.Background())

	if want := int64(len(a.retry.Intervals) + 1); requests.Load() != want {
		t.Fatalf("requests = %d, want %d", requests.Load(), want)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if got := a.lastSentCounters[models.PollCount]; got != 2 {
		t.Fatalf("lastSentCounters[PollCount] = %d, want 2 — an unsent delta must be returned", got)
	}
}

// TestReportStopsWhenContextIsCancelled verifies that a shutdown does not wait
// out the retry schedule and that the unsent delta survives it.
func TestReportStopsWhenContextIsCancelled(t *testing.T) {
	var (
		requests atomic.Int64
		cancel   context.CancelFunc
	)

	// The report is interrupted the way a shutdown interrupts it: the first
	// attempt fails and the context is cancelled while the agent is about to
	// wait for the next one.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		requests.Add(1)
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	a := New(ts.URL, time.Second, time.Second, 0)
	a.client = ts.Client()
	a.retry.Intervals = []time.Duration{time.Hour, time.Hour, time.Hour}
	seed(a)

	ctx, cancelReport := context.WithCancel(context.Background())
	cancel = cancelReport
	defer cancelReport()

	start := time.Now()
	a.reportWithRetry(ctx)

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("reportWithRetry() waited %v after cancellation", elapsed)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if got := a.lastSentCounters[models.PollCount]; got != 2 {
		t.Fatalf("lastSentCounters[PollCount] = %d, want 2 — an unsent delta must be returned", got)
	}
}

// TestSendBatchReportsStatusCode verifies that a refusal keeps its status code,
// which is what tells a retriable outage from a rejected batch.
func TestSendBatchReportsStatusCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	a := New(ts.URL, time.Second, time.Second, 0)
	a.client = ts.Client()

	err := a.sendBatch(context.Background(), []models.Metrics{gaugeMetric("Alloc", 1.5)})

	var statusErr *statusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("sendBatch() error = %v, want a *statusError", err)
	}
	if statusErr.code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", statusErr.code, http.StatusServiceUnavailable)
	}
}
