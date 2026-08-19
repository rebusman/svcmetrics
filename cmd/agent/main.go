// Command agent collects runtime and host metrics and reports them to the
// metrics server. Settings come from flags and are overridden by the ADDRESS,
// REPORT_INTERVAL, POLL_INTERVAL, BATCH_SIZE, RATE_LIMIT and KEY environment
// variables. RATE_LIMIT caps how many reports may be in flight at once. When
// KEY is non-empty, every request body is signed with HMAC-SHA256 and the
// hexadecimal digest is sent in the HashSHA256 header.
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

// logRetry reports a failed report that the agent is about to repeat, so that
// passing network or server problems show up in the agent log.
func logRetry(err error, attempt int, pause time.Duration) {
	log.Printf("Retrying report (attempt %d) in %s: %v", attempt, pause, err)
}

// logError reports a failure the agent carried on from: a host statistic it
// could not read or a report it gave up on.
func logError(err error) {
	log.Printf("Agent error: %v", err)
}

// main wires the settings together and runs the agent until the process is
// asked to stop.
func main() {
	addr := flag.String("a", "localhost:8080", "address and port to run server")
	reportInterval := flag.Int("r", 10, "report interval in seconds")
	pollInterval := flag.Int("p", 2, "poll interval in seconds")
	batchSize := flag.Int("b", agent.DefaultBatchSize, "number of metrics per batch request")
	rateLimit := flag.Int("l", agent.DefaultRateLimit, "maximum number of concurrent requests to the server")
	key := flag.String("k", "", "key for signing request bodies")
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
	if envRateLimit := os.Getenv("RATE_LIMIT"); envRateLimit != "" {
		if v, err := strconv.Atoi(envRateLimit); err == nil {
			*rateLimit = v
		} else {
			log.Printf("Invalid RATE_LIMIT value: %s, using default\n", envRateLimit)
		}
	}
	if envKey := os.Getenv("KEY"); envKey != "" {
		*key = envKey
	}

	endpoint := *addr
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a := agent.New(
		endpoint,
		time.Duration(*pollInterval)*time.Second,
		time.Duration(*reportInterval)*time.Second,
		*batchSize,
		*key,
		*rateLimit,
	)
	a.SetOnRetry(logRetry)
	a.SetOnError(logError)
	a.Run(ctx)
}
