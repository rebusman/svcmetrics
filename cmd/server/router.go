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
// as distinct patterns and clients use either.
func newRouter(log *logrus.Logger, hs repository.Storage, pinger handler.Pinger) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.CleanPath)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(requestTimeout))
	r.Use(handler.GzipRequestMiddleware)
	r.Use(handler.GzipResponseMiddleware)
	r.Use(loggingMiddleware(log))

	r.Post("/update", handler.UpdateJSONHandler(hs))
	r.Post("/updates", handler.UpdatesJSONHandler(hs))
	r.Post("/updates/", handler.UpdatesJSONHandler(hs))
	r.Post("/update/{type}/{name}/{value}", handler.UpdateHandler(hs))
	r.Get("/value/{type}/{name}", handler.ValueHandler(hs))
	r.Post("/value", handler.ValueJSONHandler(hs))
	r.Get("/ping", handler.PingHandler(pinger))
	r.Get("/", handler.ListHandler(hs))

	return r
}
