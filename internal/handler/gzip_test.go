package handler

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type countingBody struct {
	io.Reader
	closes int
}

func (b *countingBody) Close() error {
	b.closes++
	return nil
}

func gzipBytes(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(payload)); err != nil {
		t.Fatalf("gzip write error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close error = %v", err)
	}
	return buf.Bytes()
}

// Closing the replaced body must close both the decompressor and the body it
// wrapped, and it must stay at exactly one close of each even when the handler
// closes it too.
func TestGzipRequestMiddlewareClosesBothBodiesExactlyOnce(t *testing.T) {
	orig := &countingBody{Reader: bytes.NewReader(gzipBytes(t, `{"id":"Alloc"}`))}
	req := httptest.NewRequest(http.MethodPost, "/update", orig)
	req.Header.Set("Content-Encoding", "gzip")

	var got string
	h := GzipRequestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("handler read error = %v", err)
		}
		got = string(body)

		// The handler closes it, and so does the middleware's defer.
		if err := r.Body.Close(); err != nil {
			t.Errorf("first Close error = %v", err)
		}
		if err := r.Body.Close(); err != nil {
			t.Errorf("second Close error = %v", err)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got != `{"id":"Alloc"}` {
		t.Fatalf("decompressed body = %q, want %q", got, `{"id":"Alloc"}`)
	}
	if orig.closes != 1 {
		t.Fatalf("original body closed %d times, want exactly 1", orig.closes)
	}
}

// Without the handler touching it, the middleware still has to close the
// decompressor: net/http closes the body it captured before the handler ran,
// which is the original, never the replacement.
func TestGzipRequestMiddlewareClosesWhenHandlerDoesNot(t *testing.T) {
	orig := &countingBody{Reader: bytes.NewReader(gzipBytes(t, `{"id":"Alloc"}`))}
	req := httptest.NewRequest(http.MethodPost, "/update", orig)
	req.Header.Set("Content-Encoding", "gzip")

	h := GzipRequestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("handler read error = %v", err)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if orig.closes != 1 {
		t.Fatalf("original body closed %d times, want exactly 1", orig.closes)
	}
}

// Under load the connection must be reused: if request bodies were leaked, the
// server could not keep the connection alive between requests.
func TestGzipRequestMiddlewareReusesKeepAliveConnection(t *testing.T) {
	var newConns atomic.Int64

	srv := httptest.NewUnstartedServer(GzipRequestMiddleware(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Errorf("handler read error = %v", err)
			}
			w.WriteHeader(http.StatusOK)
		})))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	client := srv.Client()
	const requests = 50
	for i := range requests {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/update",
			bytes.NewReader(gzipBytes(t, `{"id":"Alloc","type":"gauge","value":1}`)))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		req.Header.Set("Content-Encoding", "gzip")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatalf("request %d drain: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, resp.StatusCode)
		}
	}

	if got := newConns.Load(); got > 1 {
		t.Fatalf("server accepted %d connections for %d requests, want 1 reused connection", got, requests)
	}
}

// Now that the middleware closes the original body, a handler that leaves the
// request unread must not cost the connection: net/http drains the leftover
// bytes after the handler returns to keep the connection alive.
func TestGzipRequestMiddlewareKeepsConnectionWhenHandlerSkipsBody(t *testing.T) {
	var newConns atomic.Int64

	srv := httptest.NewUnstartedServer(GzipRequestMiddleware(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Deliberately does not read r.Body.
			w.WriteHeader(http.StatusOK)
		})))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	client := srv.Client()
	const requests = 20
	for i := range requests {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/update",
			bytes.NewReader(gzipBytes(t, `{"id":"Alloc","type":"gauge","value":1}`)))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		req.Header.Set("Content-Encoding", "gzip")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, resp.StatusCode)
		}
	}

	if got := newConns.Load(); got > 1 {
		t.Fatalf("server accepted %d connections for %d requests with an unread body, want 1", got, requests)
	}
}

// Closing the gzip reader twice must stay harmless: it only closes the
// decompressor, never the underlying body.
func TestGzipReaderDoubleCloseIsSafe(t *testing.T) {
	orig := &countingBody{Reader: bytes.NewReader(gzipBytes(t, "payload"))}

	zr, err := gzip.NewReader(orig)
	if err != nil {
		t.Fatalf("gzip.NewReader error = %v", err)
	}
	if _, err := io.ReadAll(zr); err != nil {
		t.Fatalf("read error = %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("first Close error = %v", err)
	}
	if err := zr.Close(); err != nil {
		t.Fatalf("second Close error = %v", err)
	}
	if orig.closes != 0 {
		t.Fatalf("underlying body closed %d times, want 0", orig.closes)
	}
}

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
	if !errors.Is(err, http.ErrNotSupported) {
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
