// Agent collects runtime and host metrics and reports them to the metrics
// server in gzip-compressed batches. The collection and reporting are
// described in [github.com/rebusman/svcmetrics/internal/agent].
//
// With -crypto-key or CRYPTO_KEY, load a PEM RSA public key for request encryption.
// CRYPTO_KEY overrides the flag; an invalid key stops startup.
//
// Usage:
//
//	agent [flags]
//
// JSON settings from -c/-config or CONFIG provide defaults for flags.
// Every setting comes from a flag, and an environment variable that is set
// overrides it:
//
//	-a  ADDRESS          server address; http:// is assumed without a scheme
//	                     (localhost:8080)
//	-p  POLL_INTERVAL    seconds between metric reads (2)
//	-r  REPORT_INTERVAL  seconds between reports (10)
//	-b  BATCH_SIZE       metrics per request (32)
//	-l  RATE_LIMIT       requests in flight at once (1)
//	-k  KEY              key for HMAC-SHA256 signatures
//	-crypto-key  CRYPTO_KEY         path to a PEM RSA public key
//
// With a key, every request body is signed as sent, after compression, and the
// hexadecimal digest travels in the HashSHA256 header. When encryption is
// enabled, the signature covers the encrypted body.
//
// SIGINT and SIGTERM stop the agent.
package main

import (
	"context"
	"crypto/rsa"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rebusman/svcmetrics/internal/agent"
	"github.com/rebusman/svcmetrics/internal/config"
	payloadcrypto "github.com/rebusman/svcmetrics/internal/crypto"
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
	printBuildInfo(os.Stdout)

	addr := flag.String("a", "localhost:8080", "address and port to run server")
	reportInterval := config.Seconds(flag.CommandLine, "r", 10, "report interval (seconds or Go duration)")
	pollInterval := config.Seconds(flag.CommandLine, "p", 2, "poll interval (seconds or Go duration)")
	batchSize := flag.Int("b", agent.DefaultBatchSize, "number of metrics per batch request")
	rateLimit := flag.Int("l", agent.DefaultRateLimit, "maximum number of concurrent requests to the server")
	key := flag.String("k", "", "key for signing request bodies")
	cryptoKey := flag.String("crypto-key", "", "path to PEM RSA public key")
	configPath := config.FileFlags(flag.CommandLine)
	flag.Parse()
	if err := config.Apply(flag.CommandLine, *configPath, map[string]string{
		"address":         "a",
		"crypto_key":      "crypto-key",
		"key":             "k",
		"report_interval": "r",
		"poll_interval":   "p",
		"batch_size":      "b",
		"rate_limit":      "l",
	}); err != nil {
		log.Fatal(err)
	}
	if env := os.Getenv("CRYPTO_KEY"); env != "" {
		*cryptoKey = env
	}
	var publicKey *rsa.PublicKey
	if *cryptoKey != "" {
		var err error
		publicKey, err = payloadcrypto.LoadPublicKey(*cryptoKey)
		if err != nil {
			log.Fatalf("Failed to load crypto key: %v", err)
		}
	}

	if envAddr := os.Getenv("ADDRESS"); envAddr != "" {
		*addr = envAddr
	}

	if envReport := os.Getenv("REPORT_INTERVAL"); envReport != "" {
		if v, err := strconv.Atoi(envReport); err == nil {
			*reportInterval = time.Duration(v) * time.Second
		} else {
			log.Printf("Invalid REPORT_INTERVAL value: %s, using default\n", envReport)
		}
	}

	if envPoll := os.Getenv("POLL_INTERVAL"); envPoll != "" {
		if v, err := strconv.Atoi(envPoll); err == nil {
			*pollInterval = time.Duration(v) * time.Second
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
		*pollInterval,
		*reportInterval,
		*batchSize,
		*key,
		*rateLimit,
	)
	a.SetPublicKey(publicKey)
	a.SetOnRetry(logRetry)
	a.SetOnError(logError)
	a.Run(ctx)
}
