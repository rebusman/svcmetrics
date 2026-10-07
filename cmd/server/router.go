package main

import (
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"

	"github.com/rebusman/svcmetrics/internal/handler"
	"github.com/rebusman/svcmetrics/internal/repository"
)

// The timeouts of the server. Startup:
const (
	// dbInitTimeout bounds connecting to the database and applying the
	// migrations at startup.
	dbInitTimeout = 10 * time.Second
)

// The time budget of a request and of the shutdown that follows SIGINT or
// SIGTERM. The shutdown steps run one after another, so the process outlives
// the signal by at most shutdownBudget.
const (
	// requestTimeout bounds the time a single request may spend in a handler.
	// It leaves room for the whole retry schedule of [retry.DefaultIntervals]
	// — nine seconds of pauses plus the calls themselves — so a request is
	// never cut off while the storage is still recovering.
	requestTimeout = 12 * time.Second

	// readHeaderTimeout bounds reading the request headers, so a client that
	// opens a connection and stalls cannot hold it.
	readHeaderTimeout = 5 * time.Second

	// readTimeout bounds reading the whole request, body included.
	readTimeout = 10 * time.Second

	// responseGrace is the time an answer has to get out once the router has
	// given up on a request.
	responseGrace = 3 * time.Second

	// writeTimeout is the server write timeout. It sits above requestTimeout
	// on purpose: the router gives up on a request first and answers 504, and
	// the connection is only dropped if even that answer does not get out in
	// time.
	writeTimeout = requestTimeout + responseGrace

	// shutdownGrace is how long the server waits for the requests in flight.
	// It lasts past the router's request deadline: a handler still running
	// when the server gives up could store metrics after the audit is closed
	// and the storage is saved, and both would miss them.
	shutdownGrace = requestTimeout + responseGrace

	// auditDrainTimeout bounds the delivery of the queued audit events once
	// the server has stopped.
	auditDrainTimeout = 10 * time.Second

	// storageSaveTimeout bounds the final save of the in-memory storage.
	storageSaveTimeout = 5 * time.Second

	// shutdownBudget is the longest the process lives after the signal.
	shutdownBudget = shutdownGrace + auditDrainTimeout + storageSaveTimeout
)

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
