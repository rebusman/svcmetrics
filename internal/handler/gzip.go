package handler

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

func GzipRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "Invalid gzip content", http.StatusBadRequest)
				return
			}
			defer func() {
				_ = gz.Close()
			}()
			r.Body = io.NopCloser(gz)
		}
		next.ServeHTTP(w, r)
	})
}

func GzipResponseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gw := gzip.NewWriter(w)
		defer func() {
			_ = gw.Close()
		}()

		next.ServeHTTP(gzipResponseWriter{ResponseWriter: w, writer: gw}, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer io.Writer
}

func (grw gzipResponseWriter) WriteHeader(code int) {
	contentType := grw.ResponseWriter.Header().Get("Content-Type")
	if (contentType == "application/json" || contentType == "text/html") && grw.ResponseWriter.Header().Get("Content-Encoding") == "" {
		grw.ResponseWriter.Header().Set("Content-Encoding", "gzip")
	}
	grw.ResponseWriter.WriteHeader(code)
}

func (grw gzipResponseWriter) Write(b []byte) (int, error) {
	contentType := grw.ResponseWriter.Header().Get("Content-Type")
	if (contentType == "application/json" || contentType == "text/html") && grw.ResponseWriter.Header().Get("Content-Encoding") == "gzip" {
		return grw.writer.Write(b)
	}
	return grw.ResponseWriter.Write(b)
}
