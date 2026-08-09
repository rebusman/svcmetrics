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

// gzipRequestBody decompresses a request body and closes both the decompressor
// and the original body.
type gzipRequestBody struct {
	*gzip.Reader
	orig io.Closer

	once sync.Once
	err  error
}

// Close closes the decompressor and the original body. It is idempotent.
func (b *gzipRequestBody) Close() error {
	b.once.Do(func() {
		b.err = errors.Join(b.Reader.Close(), b.orig.Close())
	})
	return b.err
}

// GzipRequestMiddleware transparently decompresses requests that arrive with
// Content-Encoding: gzip and rejects a body that is not valid gzip with 400.
// It closes the decompressor itself: net/http closes the body it captured
// before the handler ran, which is the original one, never the replacement.
func GzipRequestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "Invalid gzip content", http.StatusBadRequest)
				return
			}

			body := &gzipRequestBody{Reader: gz, orig: r.Body}
			defer func() {
				_ = body.Close()
			}()
			r.Body = body
		}
		next.ServeHTTP(w, r)
	})
}

// GzipResponseMiddleware compresses responses of a compressible content type
// when the client accepts gzip.
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

// gzipResponseWriter compresses the body lazily: the compressor is created on
// the first write, and only if the response turned out to be compressible.
type gzipResponseWriter struct {
	http.ResponseWriter
	writer *gzip.Writer
}

// Close flushes the gzip trailer if anything was compressed.
func (grw *gzipResponseWriter) Close() error {
	if grw.writer != nil {
		return grw.writer.Close()
	}
	return nil
}

// isCompressibleContentType reports whether responses of this content type are
// worth compressing.
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

// enableCompressionIfNeeded announces gzip in the response headers before the
// status line goes out.
func (grw *gzipResponseWriter) enableCompressionIfNeeded() {
	if isCompressibleContentType(grw.ResponseWriter.Header().Get("Content-Type")) && grw.ResponseWriter.Header().Get("Content-Encoding") == "" {
		grw.ResponseWriter.Header().Set("Content-Encoding", "gzip")
		grw.ResponseWriter.Header().Del("Content-Length")
	}
}

// shouldCompress reports whether the body of this response has to be compressed.
func (grw *gzipResponseWriter) shouldCompress() bool {
	return isCompressibleContentType(grw.ResponseWriter.Header().Get("Content-Type")) && grw.ResponseWriter.Header().Get("Content-Encoding") == "gzip"
}

// WriteHeader sets the compression headers and writes the status line.
func (grw *gzipResponseWriter) WriteHeader(code int) {
	grw.enableCompressionIfNeeded()
	grw.ResponseWriter.WriteHeader(code)
}

// Write compresses b when the response is compressible and passes it through
// otherwise.
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

// Flush flushes the compressor and the underlying ResponseWriter.
func (grw *gzipResponseWriter) Flush() {
	if grw.shouldCompress() && grw.writer != nil {
		_ = grw.writer.Flush()
	}
	if flusher, ok := grw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack forwards to the underlying ResponseWriter, keeping the optional
// interface available through the wrapper.
func (grw *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := grw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	return hijacker.Hijack()
}

// Push forwards to the underlying ResponseWriter, keeping the optional
// interface available through the wrapper.
func (grw *gzipResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := grw.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}
