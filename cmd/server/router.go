package main

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rebusman/svcmetrics/internal/handler"
	"github.com/rebusman/svcmetrics/internal/repository"
	"github.com/sirupsen/logrus"
)

// requestTimeout bounds the time a single request may spend in a handler. It
// leaves room for the whole retry schedule of [retry.DefaultIntervals] — nine
// seconds of pauses plus the calls themselves — so a request is never cut off
// while the storage is still recovering, and stays below the server write
// timeout, so the deadline produces an answer instead of a dropped connection.
const requestTimeout = 12 * time.Second

// newRouter builds the HTTP router: the middleware chain plus every metric
// endpoint. Both /updates and /updates/ are registered because chi treats them
// as distinct patterns and clients use either. A non-empty key enables
// verification of request signatures and signing of response bodies. Stored
// metrics are reported to auditor, which may be nil.
//
// The signature middleware sits between the logger and the compression pair on
// purpose: outside compression, so the digest covers the bytes that actually
// travel; inside logging, so a request rejected over a bad signature still
// reaches the log.
func newRouter(log *logrus.Logger, hs repository.Storage, pinger handler.Pinger, key string, auditor handler.Auditor) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.CleanPath)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(requestTimeout))
	r.Use(loggingMiddleware(log))
	r.Use(handler.HashMiddleware(key))
	r.Use(handler.GzipRequestMiddleware)
	r.Use(handler.GzipResponseMiddleware)

	r.Post("/update", handler.UpdateJSONHandler(hs, auditor))
	r.Post("/updates", handler.UpdatesJSONHandler(hs, auditor))
	r.Post("/updates/", handler.UpdatesJSONHandler(hs, auditor))
	r.Post("/update/{type}/{name}/{value}", handler.UpdateHandler(hs, auditor))
	r.Get("/value/{type}/{name}", handler.ValueHandler(hs))
	r.Post("/value", handler.ValueJSONHandler(hs))
	r.Get("/ping", handler.PingHandler(pinger))
	r.Get("/", handler.ListHandler(hs))

	return r
}
