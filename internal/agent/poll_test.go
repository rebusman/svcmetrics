package agent

import (
	"context"
	"testing"
	"time"
)

func TestPollCancelsActiveCollection(t *testing.T) {
	a := New("", time.Millisecond, time.Second, 1, "", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan context.Context, 1)
	done := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		defer close(done)
		a.poll(ctx, func(probeCtx context.Context) error {
			select {
			case started <- probeCtx:
			default:
			}
			select {
			case <-probeCtx.Done():
				return probeCtx.Err()
			case <-release:
				return nil
			}
		})
	}()

	select {
	case probeCtx := <-started:
		if got, ok := probeCtx.Deadline(); !ok {
			t.Error("probe context has no deadline")
		} else if want, _ := ctx.Deadline(); !got.Equal(want) {
			t.Errorf("probe deadline = %v, want %v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("collection did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poll did not stop after cancellation during collection")
	}
}
