package main

import (
	"context"
	"flag"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rebusman/svcmetrics/internal/agent"
)

func main() {
	addr := flag.String("a", "localhost:8080", "address and port to run server")
	reportInterval := flag.Int("r", 10, "report interval in seconds")
	pollInterval := flag.Int("p", 2, "poll interval in seconds")
	flag.Parse()

	endpoint := *addr
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	agent.New(
		endpoint,
		time.Duration(*pollInterval)*time.Second,
		time.Duration(*reportInterval)*time.Second,
	).Run(ctx)
}
