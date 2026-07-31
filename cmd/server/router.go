package main

import (
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rebusman/svcmetrics/internal/handler"
	"github.com/rebusman/svcmetrics/internal/storage"
	"github.com/sirupsen/logrus"
)

func newRouter(log *logrus.Logger, hs storage.Storage, pinger handler.Pinger) chi.Router {
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
	r.Get("/ping", handler.PingHandler(pinger))
	r.Get("/", handler.ListHandler(hs))

	return r
}
