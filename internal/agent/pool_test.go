package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	models "github.com/rebusman/svcmetrics/internal/model"
)

// TestRateLimitCapsConcurrentRequests verifies that the pool is the rate limit:
// with more batches to send than workers, exactly rateLimit requests are in
// flight at once — no fewer, so the limit is actually used, and no more, so it
// is actually a limit.
//
// The server holds every request open until it has seen rateLimit of them at
// the same time, which makes the measurement deterministic: an agent that sent
// them one after another would never reach that point and would fall out on
// the timeout instead of on a lucky interleaving.
func TestRateLimitCapsConcurrentRequests(t *testing.T) {
	const rateLimit = 3

	var (
		inFlight    atomic.Int64
		maxInFlight atomic.Int64
		once        sync.Once
	)
	reached := make(chan struct{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		current := inFlight.Add(1)
		for {
			seen := maxInFlight.Load()
			if current <= seen || maxInFlight.CompareAndSwap(seen, current) {
				break
			}
		}
		if current >= rateLimit {
			once.Do(func() { close(reached) })
		}

		select {
		case <-reached:
		case <-time.After(5 * time.Second):
		}

		inFlight.Add(-1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// One metric per batch, so that the seeded state turns into far more
	// batches than there are workers.
	a := New(ts.URL, time.Second, time.Second, 1, "", rateLimit)
	a.client = ts.Client()
	seed(a)

	reportOnce(context.Background(), a)

	if got := maxInFlight.Load(); got != rateLimit {
		t.Fatalf("concurrent requests = %d, want exactly %d", got, rateLimit)
	}
}

// TestNewRateLimit verifies that New honours the configured rate limit and
// falls back to the default only for a non-positive value: a zero limit must
// not leave the agent without a single worker to send with.
func TestNewRateLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "configured", limit: 4, want: 4},
		{name: "zero falls back", limit: 0, want: DefaultRateLimit},
		{name: "negative falls back", limit: -2, want: DefaultRateLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := New("", 0, 0, 0, "", tt.limit).rateLimit; got != tt.want {
				t.Fatalf("rateLimit = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestRunCollectsAndReports verifies that the whole pipeline works when it is
// wired by Run: the collectors fill the state, the reporter cuts it into
// batches and the workers deliver them.
func TestRunCollectsAndReports(t *testing.T) {
	received := make(chan models.Metrics, 512)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, m := range readBatch(t, r) {
			select {
			case received <- m:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a := New(ts.URL, 10*time.Millisecond, 20*time.Millisecond, 0, "", 2)
	a.client = ts.Client()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Run(ctx)
	}()

	// Wait for the metrics of both collectors and of the counter, which is the
	// proof that the runtime poller, the host poller and the workers all ran.
	want := map[string]bool{
		"Alloc":                  false,
		models.TotalMemory:       false,
		models.CPUUtilization(1): false,
		models.PollCount:         false,
	}
	deadline := time.After(10 * time.Second)
	for remaining := len(want); remaining > 0; {
		select {
		case m := <-received:
			if seen, ok := want[m.ID]; ok && !seen {
				want[m.ID] = true
				remaining--
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the metrics; seen so far: %v", want)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after the context was cancelled")
	}
}

// TestRunStopsOnCancelledContext verifies that a shutdown ends the agent even
// while the workers are stuck on a server that never answers: Run must not
// wait for the requests it can no longer deliver.
func TestRunStopsOnCancelledContext(t *testing.T) {
	blocked := make(chan struct{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-blocked:
		case <-r.Context().Done():
		}
	}))
	// Releasing the handlers has to happen before Close waits for them.
	defer ts.Close()
	defer close(blocked)

	a := New(ts.URL, time.Millisecond, time.Millisecond, 1, "", 2)
	a.client = ts.Client()
	seed(a)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Run(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after the context was cancelled")
	}
}
