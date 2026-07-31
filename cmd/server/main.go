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
	"github.com/rebusman/svcmetrics/internal/storage"
	"github.com/sirupsen/logrus"
)

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	bodySize   int
	// writeErr holds the first failed body write. The handlers cannot report
	// it themselves — by then the status line is already on the wire — so it
	// is recorded here and logged with the rest of the request.
	writeErr error
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	size, err := rw.ResponseWriter.Write(b)
	rw.bodySize += size
	if err != nil && rw.writeErr == nil {
		rw.writeErr = err
	}
	return size, err
}

// Flush and Hijack keep working through the wrapper: wrapping a ResponseWriter
// hides whatever optional interfaces it implements, and the gzip writer below
// expects both to survive the decoration.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	return hijacker.Hijack()
}

// syncStorage decorates MemStorage to persist metrics to disk synchronously
// after every write. It is used when STORE_INTERVAL == 0.
type syncStorage struct {
	*storage.MemStorage
	path string
	log  *logrus.Logger
}

var _ storage.Storage = (*syncStorage)(nil)

func (s *syncStorage) save() {
	if err := s.Save(s.path); err != nil {
		s.log.Errorf("Failed to save metrics to %s: %v", s.path, err)
	}
}

func (s *syncStorage) UpdateGauge(ctx context.Context, name string, value float64) (float64, error) {
	stored, err := s.MemStorage.UpdateGauge(ctx, name, value)
	if err != nil {
		return 0, err
	}
	s.save()
	return stored, nil
}

func (s *syncStorage) UpdateCounter(ctx context.Context, name string, value int64) (int64, error) {
	stored, err := s.MemStorage.UpdateCounter(ctx, name, value)
	if err != nil {
		return 0, err
	}
	s.save()
	return stored, nil
}

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

			// A failed body write is not something the response can convey —
			// the client already got the status — so it is only worth a log
			// line, at a level that stands out from the successful requests.
			if rw.writeErr != nil {
				entry.WithError(rw.writeErr).Error("Failed to write response body")
				return
			}

			entry.Info("Request handled")
		})
	}
}

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

	// hs is the storage the handlers work through. Preference order: PostgreSQL,
	// then a file-backed in-memory storage, then plain memory.
	var (
		hs     storage.Storage
		pinger handler.Pinger
		// fileStore is non-nil only when metrics are persisted to a file; the
		// periodic saver and the shutdown flush use it.
		fileStore *storage.MemStorage
	)

	switch {
	case *databaseDSN != "":
		initCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pg, err := storage.NewPgStorage(initCtx, *databaseDSN)
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
		s := storage.NewMemStorage()
		if *restore {
			if err := s.Load(*fileStoragePath); err != nil {
				log.Errorf("Failed to restore metrics from %s: %v", *fileStoragePath, err)
			} else {
				log.Infof("Metrics restored from %s", *fileStoragePath)
			}
		}
		fileStore = s

		// With STORE_INTERVAL == 0 every write is flushed to disk
		// synchronously; otherwise a background ticker persists metrics
		// periodically.
		hs = s
		if *storeInterval == 0 {
			hs = &syncStorage{MemStorage: s, path: *fileStoragePath, log: log}
		}
		log.Infof("Using in-memory storage persisted to %s", *fileStoragePath)

	default:
		hs = storage.NewMemStorage()
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
					// The shutdown flush happens in main, not here: this
					// goroutine has no chance to finish a write once main
					// returns and the process exits.
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

	// Persist the final state once the server has stopped accepting requests.
	// Done here rather than in the ticker goroutine so that the process cannot
	// exit while the write is still in flight.
	if fileStore != nil {
		if err := fileStore.Save(*fileStoragePath); err != nil {
			log.Errorf("Failed to save metrics to %s on shutdown: %v", *fileStoragePath, err)
		}
	}
}
