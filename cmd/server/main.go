// Command server serves the metrics HTTP API and persists the metrics in
// PostgreSQL, in a JSON file or in memory. Settings come from flags and are
// overridden by the ADDRESS, STORE_INTERVAL, FILE_STORAGE_PATH, RESTORE and
// DATABASE_DSN environment variables.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rebusman/svcmetrics/internal/handler"
	models "github.com/rebusman/svcmetrics/internal/model"
	"github.com/rebusman/svcmetrics/internal/repository"
	"github.com/sirupsen/logrus"
)

// responseWriter records the status code, the body size and the first failed
// body write so that the logging middleware can report them. The handlers
// cannot report a write failure themselves: by then the status line is already
// on the wire.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	bodySize   int
	writeErr   error
}

// WriteHeader records the status code and writes the status line.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Write records the body size and the first write failure.
func (rw *responseWriter) Write(b []byte) (int, error) {
	size, err := rw.ResponseWriter.Write(b)
	rw.bodySize += size
	if err != nil && rw.writeErr == nil {
		rw.writeErr = err
	}
	return size, err
}

// Flush forwards to the underlying ResponseWriter. Wrapping a ResponseWriter
// hides the optional interfaces it implements, and the gzip writer expects this
// one to survive the decoration.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter, keeping the optional
// interface available through the wrapper.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	return hijacker.Hijack()
}

// syncStorage decorates MemStorage to persist the metrics to disk synchronously
// after every write. It is used when STORE_INTERVAL is 0.
type syncStorage struct {
	*repository.MemStorage
	path string
	log  *logrus.Logger
}

var _ repository.Storage = (*syncStorage)(nil)

// save writes a snapshot to disk, logging a failure rather than failing the
// request.
func (s *syncStorage) save() {
	if err := s.Save(s.path); err != nil {
		s.log.Errorf("Failed to save metrics to %s: %v", s.path, err)
	}
}

// UpdateGauge stores the gauge and flushes the snapshot to disk.
func (s *syncStorage) UpdateGauge(ctx context.Context, name string, value float64) (float64, error) {
	stored, err := s.MemStorage.UpdateGauge(ctx, name, value)
	if err != nil {
		return 0, err
	}
	s.save()
	return stored, nil
}

// UpdateCounter stores the counter and flushes the snapshot to disk.
func (s *syncStorage) UpdateCounter(ctx context.Context, name string, value int64) (int64, error) {
	stored, err := s.MemStorage.UpdateCounter(ctx, name, value)
	if err != nil {
		return 0, err
	}
	s.save()
	return stored, nil
}

// UpdateBatch stores the batch and flushes the snapshot to disk once for the
// whole batch rather than once per metric.
func (s *syncStorage) UpdateBatch(ctx context.Context, metrics []models.Metrics) error {
	if err := s.MemStorage.UpdateBatch(ctx, metrics); err != nil {
		return err
	}
	if len(metrics) > 0 {
		s.save()
	}
	return nil
}

// loggingMiddleware logs one line per request with its method, URI, status,
// body size and duration. A failed body write cannot be conveyed to the client,
// so it is logged at a louder level instead.
func loggingMiddleware(log *logrus.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(rw, r)

			duration := time.Since(start)

			entry := log.WithFields(logrus.Fields{
				"uri":         r.RequestURI,
				"method":      r.Method,
				"duration":    duration,
				"status_code": rw.statusCode,
				"body_size":   rw.bodySize,
			})

			if rw.writeErr != nil {
				entry.WithError(rw.writeErr).Error("Failed to write response body")
				return
			}

			entry.Info("Request handled")
		})
	}
}

// main reads the settings, picks a storage — PostgreSQL, then a file-backed
// in-memory one, then plain memory — and serves until the process is asked to
// stop. The final snapshot is written here rather than in the saver goroutine,
// so the process cannot exit while that write is still in flight.
func main() {
	addr := flag.String("a", "localhost:8080", "address and port to run server")
	storeInterval := flag.Int("i", 300, "save interval in seconds")
	fileStoragePath := flag.String("f", "metrics_storage.json", "path to storage file")
	restore := flag.Bool("r", false, "restore metrics from file on startup")
	databaseDSN := flag.String("d", "", "PostgreSQL connection string (DSN)")
	flag.Parse()

	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	if envAddr := os.Getenv("ADDRESS"); envAddr != "" {
		*addr = envAddr
	}

	if envStoreInterval := os.Getenv("STORE_INTERVAL"); envStoreInterval != "" {
		v, err := strconv.Atoi(envStoreInterval)
		if err != nil {
			log.Fatalf("Invalid STORE_INTERVAL value %q: %v", envStoreInterval, err)
		}
		*storeInterval = v
	}

	if envFileStoragePath := os.Getenv("FILE_STORAGE_PATH"); envFileStoragePath != "" {
		*fileStoragePath = envFileStoragePath
	}

	if envRestore := os.Getenv("RESTORE"); envRestore != "" {
		v, err := strconv.ParseBool(envRestore)
		if err != nil {
			log.Fatalf("Invalid RESTORE value %q: %v", envRestore, err)
		}
		*restore = v
	}

	if envDatabaseDSN := os.Getenv("DATABASE_DSN"); envDatabaseDSN != "" {
		*databaseDSN = envDatabaseDSN
	}

	var (
		hs        repository.Storage
		pinger    handler.Pinger
		fileStore *repository.MemStorage
	)

	switch {
	case *databaseDSN != "":
		initCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pg, err := repository.NewPgStorage(initCtx, *databaseDSN)
		cancel()
		if err != nil {
			log.Fatalf("Failed to initialize database storage: %v", err)
		}
		defer func() {
			if err := pg.Close(); err != nil {
				log.Errorf("Failed to close database connection: %v", err)
			}
		}()
		hs = pg
		pinger = pg
		log.Info("Using PostgreSQL storage")

	case *fileStoragePath != "":
		s := repository.NewMemStorage()
		if *restore {
			if err := s.Load(*fileStoragePath); err != nil {
				log.Errorf("Failed to restore metrics from %s: %v", *fileStoragePath, err)
			} else {
				log.Infof("Metrics restored from %s", *fileStoragePath)
			}
		}
		fileStore = s

		hs = s
		if *storeInterval == 0 {
			hs = &syncStorage{MemStorage: s, path: *fileStoragePath, log: log}
		}
		log.Infof("Using in-memory storage persisted to %s", *fileStoragePath)

	default:
		hs = repository.NewMemStorage()
		log.Info("Using in-memory storage")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	r := newRouter(log, hs, pinger)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	if fileStore != nil && *storeInterval > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(*storeInterval) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := fileStore.Save(*fileStoragePath); err != nil {
						log.Errorf("Failed to save metrics to %s: %v", *fileStoragePath, err)
					}
				}
			}
		}()
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := srv.Shutdown(shutdownCtx)
	cancel()
	if err != nil {
		log.Errorf("Server shutdown failed: %v", err)
	}

	if fileStore != nil {
		if err := fileStore.Save(*fileStoragePath); err != nil {
			log.Errorf("Failed to save metrics to %s on shutdown: %v", *fileStoragePath, err)
		}
	}
}
