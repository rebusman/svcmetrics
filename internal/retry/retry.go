// Package retry repeats an operation that failed with a retriable error — one
// caused by a condition that is expected to pass by itself, such as a dropped
// database connection or an unreachable server.
//
// An operation is described by a [Config], which says how long to wait between
// the attempts and which failures deserve another one, and is run by [Do] or,
// when it produces a value, by [DoValue].
package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

// DefaultIntervals holds the pauses taken before each repetition, and is used
// by every [Config] that does not name its own schedule. Its length is also the
// number of repetitions, so an operation runs at most four times: the original
// call plus a retry after one, three and five seconds.
var DefaultIntervals = []time.Duration{
	1 * time.Second,
	3 * time.Second,
	5 * time.Second,
}

// IsRetriable reports whether a failure is worth repeating. It is called only
// with a non-nil error.
type IsRetriable func(error) bool

// OnRetry observes a failed attempt that is about to be repeated. It receives
// the error of the attempt, its one-based number and the pause taken before
// the next one. A typical implementation logs the failure so that passing
// problems do not stay invisible.
type OnRetry func(err error, attempt int, pause time.Duration)

// Config describes how an operation is repeated. The zero value retries every
// error on the default schedule, which is rarely what a caller wants: an
// [IsRetriable] that returns false for permanent failures keeps a request from
// waiting nine seconds for an answer that will not change.
type Config struct {
	// Intervals overrides [DefaultIntervals] when it is non-nil. An empty
	// non-nil slice disables retrying altogether.
	Intervals []time.Duration

	// Retriable classifies the failures. A nil check treats every error as
	// retriable.
	Retriable IsRetriable

	// OnRetry, when non-nil, is called before every repetition with the error
	// of the failed attempt, its one-based number and the pause about to be
	// taken. A nil OnRetry repeats silently.
	OnRetry OnRetry
}

// intervals returns the schedule c runs on.
func (c Config) intervals() []time.Duration {
	if c.Intervals == nil {
		return DefaultIntervals
	}
	return c.Intervals
}

// retriable reports whether err is worth repeating under c.
func (c Config) retriable(err error) bool {
	if c.Retriable == nil {
		return true
	}
	return c.Retriable(err)
}

// Do runs fn until it succeeds, until it fails with an error cfg calls
// permanent, or until the schedule runs out, and returns the error of the last
// attempt. A cancelled context ends the waiting at once, so a caller that gives
// up never pays for the pauses that were still ahead.
func Do(ctx context.Context, cfg Config, fn func(ctx context.Context) error) error {
	_, err := DoValue(ctx, cfg, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}

// DoValue is [Do] for an operation that produces a value. On failure it
// returns whatever the last attempt returned alongside its error.
func DoValue[T any](ctx context.Context, cfg Config, fn func(ctx context.Context) (T, error)) (T, error) {
	intervals := cfg.intervals()

	for attempt := 0; ; attempt++ {
		res, err := fn(ctx)
		if err == nil {
			return res, nil
		}
		if attempt >= len(intervals) || !cfg.retriable(err) {
			return res, err
		}
		pause := withJitter(intervals[attempt])
		if cfg.OnRetry != nil {
			cfg.OnRetry(err, attempt+1, pause)
		}
		if !wait(ctx, pause) {
			return res, err
		}
	}
}

// withJitter stretches d by a random fraction of up to a quarter of its
// length, so that many clients recovering from the same outage do not repeat
// their attempts in lockstep.
func withJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d + rand.N(d/4+1)
}

// wait sleeps for d and reports whether it slept through: a cancelled context
// returns false and the caller stops retrying.
func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
