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

// Pools of the decompressors and compressors used by the middleware. A
// compressor holds several hundred kilobytes of tables and a decompressor tens
// of kilobytes, so creating them per request dominated the memory the server
// allocated. A pooled one is always pointed away from the request it served
// before it goes back, so the pool never pins a request or a connection.
var (
	gzipReaderPool sync.Pool // *gzipReader
	gzipWriterPool = sync.Pool{
		New: func() any { return gzip.NewWriter(io.Discard) },
	}
)

// gzipReader is a pooled decompressor together with the buffered reader it
// reads through. [gzip.Reader.Reset] wraps a source that is not an
// [io.ByteReader] — and a request body is not — in a fresh [bufio.Reader], so
// keeping that buffer in the pool as well is what makes the reuse free.
type gzipReader struct {
	*gzip.Reader
	buf *bufio.Reader
}

// getGzipReader returns a pooled decompressor reading r, or the error of a body
// that does not start with a gzip header.
func getGzipReader(r io.Reader) (*gzipReader, error) {
	zr, ok := gzipReaderPool.Get().(*gzipReader)
	if !ok {
		zr = &gzipReader{Reader: new(gzip.Reader), buf: bufio.NewReader(r)}
	} else {
		zr.buf.Reset(r)
	}
	if err := zr.Reset(zr.buf); err != nil {
		putGzipReader(zr)
		return nil, err
	}
	return zr, nil
}

// putGzipReader detaches zr from the body it read and returns it to the pool.
// The decompressor keeps pointing at the buffer, which no longer points at the
// body; the next user resets both.
func putGzipReader(zr *gzipReader) {
	zr.buf.Reset(nil)
	gzipReaderPool.Put(zr)
}

// gzipRequestBody decompresses a request body and closes both the decompressor
// and the original body. Closing it returns the decompressor to the pool, so
// it must not be read after Close.
type gzipRequestBody struct {
	*gzipReader
	orig io.Closer

	once sync.Once
	err  error
}

// Close closes the decompressor and the original body. It is idempotent.
func (b *gzipRequestBody) Close() error {
	b.once.Do(func() {
		b.err = errors.Join(b.gzipReader.Close(), b.orig.Close())
		putGzipReader(b.gzipReader)
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
			gz, err := getGzipReader(r.Body)
			if err != nil {
				http.Error(w, "Invalid gzip content", http.StatusBadRequest)
				return
			}

			body := &gzipRequestBody{gzipReader: gz, orig: r.Body}
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

// Close flushes the gzip trailer if anything was compressed and returns the
// compressor to the pool.
func (grw *gzipResponseWriter) Close() error {
	if grw.writer == nil {
		return nil
	}
	err := grw.writer.Close()
	grw.writer.Reset(io.Discard)
	gzipWriterPool.Put(grw.writer)
	grw.writer = nil
	return err
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
			grw.writer = gzipWriterPool.Get().(*gzip.Writer)
			grw.writer.Reset(grw.ResponseWriter)
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
