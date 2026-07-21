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

	r := chi.NewRouter()
	r.Use(middleware.CleanPath)
	r.Use(middleware.Recoverer)
	r.Use(handler.GzipRequestMiddleware)
	r.Use(handler.GzipResponseMiddleware)
	r.Use(loggingMiddleware(log))

	r.Post("/update", handler.UpdateJSONHandler(s))
	r.Post("/update/{type}/{name}/{value}", handler.UpdateHandler(s))
	r.Get("/value/{type}/{name}", handler.ValueHandler(s))
	r.Post("/value", handler.ValueJSONHandler(s))
	r.Get("/", handler.ListHandler(s))

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
