package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fast is the schedule the tests run on: three retries that cost no wall clock.
var fast = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}

var errBoom = errors.New("boom")

func TestDoStopsOnFirstSuccess(t *testing.T) {
	var calls int

	err := Do(context.Background(), Config{Intervals: fast}, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// TestDoRetriesUntilSuccess verifies that a failure the config accepts as
// retriable is repeated and that the eventual success is reported.
func TestDoRetriesUntilSuccess(t *testing.T) {
	var calls int

	err := Do(context.Background(), Config{Intervals: fast}, func(context.Context) error {
		calls++
		if calls < 3 {
			return errBoom
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

// TestDoExhaustsSchedule verifies that an operation runs once per interval plus
// once for the original attempt, and that the last error is what comes back.
func TestDoExhaustsSchedule(t *testing.T) {
	var calls int

	err := Do(context.Background(), Config{Intervals: fast}, func(context.Context) error {
		calls++
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Do() error = %v, want %v", err, errBoom)
	}
	if want := len(fast) + 1; calls != want {
		t.Fatalf("calls = %d, want %d", calls, want)
	}
}

// TestDoDoesNotRetryPermanentError verifies that a failure the config rejects
// costs a single attempt: a caller waiting on a request must not sit through
// the whole schedule for an answer that cannot change.
func TestDoDoesNotRetryPermanentError(t *testing.T) {
	var calls int

	cfg := Config{
		Intervals: []time.Duration{time.Hour},
		Retriable: func(error) bool { return false },
	}
	err := Do(context.Background(), cfg, func(context.Context) error {
		calls++
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Do() error = %v, want %v", err, errBoom)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// TestDoStopsWhenContextIsCancelled verifies that a cancelled context ends the
// waiting at once instead of sleeping out the remaining schedule.
func TestDoStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int

	start := time.Now()
	err := Do(ctx, Config{Intervals: []time.Duration{time.Hour, time.Hour}}, func(context.Context) error {
		calls++
		cancel()
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Do() error = %v, want %v", err, errBoom)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Do() waited %v after cancellation", elapsed)
	}
}

// TestDoValueReturnsLastResult verifies that DoValue hands back the value of
// the attempt that succeeded.
func TestDoValueReturnsLastResult(t *testing.T) {
	var calls int

	got, err := DoValue(context.Background(), Config{Intervals: fast}, func(context.Context) (int, error) {
		calls++
		if calls < 2 {
			return 0, errBoom
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("DoValue() error = %v", err)
	}
	if got != 42 {
		t.Fatalf("DoValue() = %d, want 42", got)
	}
}

// TestEmptyIntervalsDisableRetrying verifies that a non-nil empty schedule
// means "attempt once", while a nil one falls back to the default.
func TestEmptyIntervalsDisableRetrying(t *testing.T) {
	var calls int

	err := Do(context.Background(), Config{Intervals: []time.Duration{}}, func(context.Context) error {
		calls++
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Do() error = %v, want %v", err, errBoom)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	if got := (Config{}).intervals(); len(got) != len(DefaultIntervals) {
		t.Fatalf("zero config schedule = %v, want %v", got, DefaultIntervals)
	}
}

// TestDefaultIntervalsFollowTheIncrement pins the schedule the task asks for:
// one, three and five seconds.
func TestDefaultIntervalsFollowTheIncrement(t *testing.T) {
	want := []time.Duration{time.Second, 3 * time.Second, 5 * time.Second}

	if len(DefaultIntervals) != len(want) {
		t.Fatalf("DefaultIntervals = %v, want %v", DefaultIntervals, want)
	}
	for i, d := range want {
		if DefaultIntervals[i] != d {
			t.Errorf("DefaultIntervals[%d] = %v, want %v", i, DefaultIntervals[i], d)
		}
	}
}

// TestOnRetryObservesEveryRepetition verifies that the hook fires once per
// repetition — not for the original attempt and not for the final failure —
// and receives the error and the one-based attempt number.
func TestOnRetryObservesEveryRepetition(t *testing.T) {
	var (
		calls    int
		observed []int
	)

	cfg := Config{
		Intervals: []time.Duration{time.Millisecond, time.Millisecond},
		OnRetry: func(err error, attempt int, pause time.Duration) {
			if !errors.Is(err, errBoom) {
				t.Errorf("OnRetry error = %v, want errBoom", err)
			}
			if pause < time.Millisecond {
				t.Errorf("OnRetry pause = %v, want at least the interval", pause)
			}
			observed = append(observed, attempt)
		},
	}

	err := Do(context.Background(), cfg, func(context.Context) error {
		calls++
		return errBoom
	})

	if !errors.Is(err, errBoom) {
		t.Fatalf("Do() error = %v, want errBoom", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if len(observed) != 2 || observed[0] != 1 || observed[1] != 2 {
		t.Fatalf("observed attempts = %v, want [1 2]", observed)
	}
}

// TestWithJitterStaysWithinBounds pins the jitter contract: the pause is never
// shorter than the interval and never stretches it by more than a quarter.
func TestWithJitterStaysWithinBounds(t *testing.T) {
	const d = 4 * time.Second
	for range 1000 {
		got := withJitter(d)
		if got < d || got > d+d/4 {
			t.Fatalf("withJitter(%v) = %v, want within [%v, %v]", d, got, d, d+d/4)
		}
	}

	if got := withJitter(0); got != 0 {
		t.Fatalf("withJitter(0) = %v, want 0", got)
	}
}
