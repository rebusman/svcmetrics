package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rebusman/svcmetrics/internal/handler"
	"github.com/rebusman/svcmetrics/internal/storage"
	"github.com/sirupsen/logrus"
)

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	bodySize   int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	size, err := rw.ResponseWriter.Write(b)
	rw.bodySize += size
	return size, err
}

// syncStorage decorates MemStorage to persist metrics to disk synchronously
// after every write. It is used when STORE_INTERVAL == 0.
type syncStorage struct {
	*storage.MemStorage
	path string
	log  *logrus.Logger
}

func (s *syncStorage) save() {
	if err := s.MemStorage.Save(s.path); err != nil {
		s.log.Errorf("Failed to save metrics to %s: %v", s.path, err)
	}
}

func (s *syncStorage) UpdateGauge(name string, value float64) {
	s.MemStorage.UpdateGauge(name, value)
	s.save()
}

func (s *syncStorage) UpdateCounter(name string, value int64) {
	s.MemStorage.UpdateCounter(name, value)
	s.save()
}

func loggingMiddleware(log *logrus.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(rw, r)

			duration := time.Since(start)

			log.WithFields(logrus.Fields{
				"uri":         r.RequestURI,
				"method":      r.Method,
				"duration":    duration,
				"status_code": rw.statusCode,
				"body_size":   rw.bodySize,
			}).Info("Request handled")
		})
	}
}

func main() {
	addr := flag.String("a", "localhost:8080", "address and port to run server")
	storeInterval := flag.Int("i", 300, "save interval in seconds")
	fileStoragePath := flag.String("f", "metrics_storage.json", "path to storage file")
	restore := flag.Bool("r", false, "restore metrics from file on startup")
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s := storage.NewMemStorage()
	if *restore {
		if err := s.Load(*fileStoragePath); err != nil {
			log.Errorf("Failed to restore metrics from %s: %v", *fileStoragePath, err)
		} else {
			log.Infof("Metrics restored from %s", *fileStoragePath)
		}
	}

	// hs is the storage the handlers write through. With STORE_INTERVAL == 0
	// every write is flushed to disk synchronously; otherwise a background
	// ticker persists metrics periodically.
	var hs handler.Storage = s
	if *storeInterval == 0 {
		hs = &syncStorage{MemStorage: s, path: *fileStoragePath, log: log}
	}

	r := chi.NewRouter()
	r.Use(middleware.CleanPath)
	r.Use(middleware.Recoverer)
	r.Use(handler.GzipRequestMiddleware)
	r.Use(handler.GzipResponseMiddleware)
	r.Use(loggingMiddleware(log))

	r.Post("/update", handler.UpdateJSONHandler(hs))
	r.Post("/update/{type}/{name}/{value}", handler.UpdateHandler(hs))
	r.Get("/value/{type}/{name}", handler.ValueHandler(hs))
	r.Post("/value", handler.ValueJSONHandler(hs))
	r.Get("/", handler.ListHandler(hs))

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

	if *storeInterval > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(*storeInterval) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					if err := s.Save(*fileStoragePath); err != nil {
						log.Errorf("Failed to save metrics to %s on shutdown: %v", *fileStoragePath, err)
					}
					return
				case <-ticker.C:
					if err := s.Save(*fileStoragePath); err != nil {
						log.Errorf("Failed to save metrics to %s: %v", *fileStoragePath, err)
					}
				}
			}
		}()
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal(err)
	}
}
