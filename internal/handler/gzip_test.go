package handler

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, bufio.NewReadWriter(bufio.NewReader(bytes.NewReader(nil)), bufio.NewWriter(bytes.NewBuffer(nil))), nil
}

func TestGzipResponseWriterFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	grw := &gzipResponseWriter{ResponseWriter: rec, writer: gw}

	rec.Header().Set("Content-Type", "application/json")
	rec.Header().Set("Content-Encoding", "gzip")

	if _, err := grw.Write([]byte("hello")); err != nil {
		t.Fatalf("Write error = %v", err)
	}
	grw.Flush()
	if err := gw.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("gzip.NewReader error = %v", err)
	}
	defer func() { _ = zr.Close() }()

	decoded := make([]byte, 5)
	if _, err := zr.Read(decoded); err != nil {
		t.Fatalf("read gzipped data error = %v", err)
	}
	if string(decoded) != "hello" {
		t.Fatalf("decoded body = %q, want %q", string(decoded), "hello")
	}
}

func TestGzipResponseWriterHijackProxy(t *testing.T) {
	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	grw := &gzipResponseWriter{ResponseWriter: rec, writer: gzip.NewWriter(bytes.NewBuffer(nil))}

	_, _, err := grw.Hijack()
	if err != nil {
		t.Fatalf("Hijack error = %v", err)
	}
	if !rec.hijacked {
		t.Fatal("expected underlying Hijack to be called")
	}
}

func TestGzipResponseWriterPushNotSupported(t *testing.T) {
	rec := httptest.NewRecorder()
	grw := &gzipResponseWriter{ResponseWriter: rec, writer: gzip.NewWriter(bytes.NewBuffer(nil))}

	err := grw.Push("/asset.js", nil)
	if err != http.ErrNotSupported {
		t.Fatalf("Push error = %v, want %v", err, http.ErrNotSupported)
	}
}

func TestGzipResponseWriterCompressesWithCharset(t *testing.T) {
	rec := httptest.NewRecorder()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	grw := &gzipResponseWriter{ResponseWriter: rec, writer: gw}

	rec.Header().Set("Content-Type", "application/json; charset=utf-8")
	if _, err := grw.Write([]byte("hello")); err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("gzip.NewReader error = %v", err)
	}
	defer func() { _ = zr.Close() }()

	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzipped data error = %v", err)
	}
	if string(decoded) != "hello" {
		t.Fatalf("decoded body = %q, want %q", string(decoded), "hello")
	}
}

func TestGzipResponseWriterWriteHeaderClearsContentLength(t *testing.T) {
	rec := httptest.NewRecorder()
	grw := &gzipResponseWriter{ResponseWriter: rec, writer: gzip.NewWriter(bytes.NewBuffer(nil))}

	rec.Header().Set("Content-Type", "text/html; charset=utf-8")
	rec.Header().Set("Content-Length", "5")

	grw.WriteHeader(http.StatusOK)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want empty", got)
	}
}

func TestGzipResponseMiddlewareSetsVaryHeader(t *testing.T) {
	h := GzipResponseMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	vary := rec.Header().Values("Vary")
	found := false
	for _, v := range vary {
		for _, part := range strings.Split(v, ",") {
			if strings.TrimSpace(part) == "Accept-Encoding" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("Vary header = %v, want contains Accept-Encoding", vary)
	}
}
