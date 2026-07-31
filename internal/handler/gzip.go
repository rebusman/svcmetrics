package handler

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
)

type gzipRequestBody struct {
	*gzip.Reader
	orig io.Closer

	once sync.Once
	err  error
}

func (b *gzipRequestBody) Close() error {
	b.once.Do(func() {
		b.err = errors.Join(b.Reader.Close(), b.orig.Close())
	})
	return b.err
}

func GzipRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "Invalid gzip content", http.StatusBadRequest)
				return
			}

			body := &gzipRequestBody{Reader: gz, orig: r.Body}
			// net/http closes the body it captured before the handler ran, not
			// the replacement, so without this the decompressor would never be
			// closed at all. Close being idempotent keeps that safe.
			defer func() {
				_ = body.Close()
			}()
			r.Body = body
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

		w.Header().Add("Vary", "Accept-Encoding")

		grw := &gzipResponseWriter{ResponseWriter: w}
		defer func() {
			_ = grw.Close()
		}()

		next.ServeHTTP(grw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer *gzip.Writer
}

func (grw *gzipResponseWriter) Close() error {
	if grw.writer != nil {
		return grw.writer.Close()
	}
	return nil
}

func isCompressibleContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil {
		return mediaType == "application/json" || mediaType == "text/html"
	}
	return strings.HasPrefix(contentType, "application/json") || strings.HasPrefix(contentType, "text/html")
}

func (grw *gzipResponseWriter) enableCompressionIfNeeded() {
	if isCompressibleContentType(grw.ResponseWriter.Header().Get("Content-Type")) && grw.ResponseWriter.Header().Get("Content-Encoding") == "" {
		grw.ResponseWriter.Header().Set("Content-Encoding", "gzip")
		grw.ResponseWriter.Header().Del("Content-Length")
	}
}

func (grw *gzipResponseWriter) shouldCompress() bool {
	return isCompressibleContentType(grw.ResponseWriter.Header().Get("Content-Type")) && grw.ResponseWriter.Header().Get("Content-Encoding") == "gzip"
}

func (grw *gzipResponseWriter) WriteHeader(code int) {
	grw.enableCompressionIfNeeded()
	grw.ResponseWriter.WriteHeader(code)
}

func (grw *gzipResponseWriter) Write(b []byte) (int, error) {
	grw.enableCompressionIfNeeded()
	if grw.shouldCompress() {
		if grw.writer == nil {
			grw.writer = gzip.NewWriter(grw.ResponseWriter)
		}
		return grw.writer.Write(b)
	}
	return grw.ResponseWriter.Write(b)
}

func (grw *gzipResponseWriter) Flush() {
	if grw.shouldCompress() && grw.writer != nil {
		_ = grw.writer.Flush()
	}
	if flusher, ok := grw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (grw *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := grw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	return hijacker.Hijack()
}

func (grw *gzipResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := grw.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}
