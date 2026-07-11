package main

import (
	"context"
	"os/signal"
	"syscall"
	"time"

	"github.com/rebusman/svcmetrics/internal/agent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	agent.New("http://localhost:8080", 2*time.Second, 10*time.Second).Run(ctx)
}
