// Command agent collects runtime metrics and reports them to the metrics
// server. Settings come from flags and are overridden by the ADDRESS,
// REPORT_INTERVAL, POLL_INTERVAL and BATCH_SIZE environment variables.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rebusman/svcmetrics/internal/agent"
)

// main wires the settings together and runs the agent until the process is
// asked to stop.
func main() {
	addr := flag.String("a", "localhost:8080", "address and port to run server")
	reportInterval := flag.Int("r", 10, "report interval in seconds")
	pollInterval := flag.Int("p", 2, "poll interval in seconds")
	batchSize := flag.Int("b", agent.DefaultBatchSize, "number of metrics per batch request")
	flag.Parse()

	if envAddr := os.Getenv("ADDRESS"); envAddr != "" {
		*addr = envAddr
	}

	if envReport := os.Getenv("REPORT_INTERVAL"); envReport != "" {
		if v, err := strconv.Atoi(envReport); err == nil {
			*reportInterval = v
		} else {
			log.Printf("Invalid REPORT_INTERVAL value: %s, using default\n", envReport)
		}
	}

	if envPoll := os.Getenv("POLL_INTERVAL"); envPoll != "" {
		if v, err := strconv.Atoi(envPoll); err == nil {
			*pollInterval = v
		} else {
			log.Printf("Invalid POLL_INTERVAL value: %s, using default\n", envPoll)
		}
	}

	if envBatchSize := os.Getenv("BATCH_SIZE"); envBatchSize != "" {
		if v, err := strconv.Atoi(envBatchSize); err == nil {
			*batchSize = v
		} else {
			log.Printf("Invalid BATCH_SIZE value: %s, using default\n", envBatchSize)
		}
	}

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
		*batchSize,
	).Run(ctx)
}
