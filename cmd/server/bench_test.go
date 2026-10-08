package main

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/rebusman/svcmetrics/internal/agent"
	"github.com/rebusman/svcmetrics/internal/audit"
	"github.com/rebusman/svcmetrics/internal/repository"
)

// discardObserver accepts every audit event and keeps none, so the benchmark
// measures the audit path of the server and not a receiver.
type discardObserver struct{}

func (discardObserver) Name() string                              { return "discard" }
func (discardObserver) Update(context.Context, audit.Event) error { return nil }

// BenchmarkAgentReport measures the system end to end: the agent reads the
// runtime and reports the batch, gzip-compressed and signed, to the production
// router, which verifies it, decompresses it, stores it in memory and audits
// it. The memory profiles in profiles/ are taken from it; see README.md.
func BenchmarkAgentReport(b *testing.B) {
	const key = "secret"

	log := logrus.New()
	log.SetOutput(io.Discard)

	publisher := audit.NewPublisher(nil)
	publisher.Register(discardObserver{})
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = publisher.Close(ctx)
	})

	srv := httptest.NewServer(newRouter(log, repository.NewMemStorage(), nil, key, publisher))
	b.Cleanup(srv.Close)

	a := agent.New(srv.URL, time.Second, time.Second, 0, key, 0)
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		a.CollectRuntimeMetrics()
		if err := a.SendMetrics(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
