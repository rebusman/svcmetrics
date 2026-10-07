package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is an observer that remembers what it received.
type recorder struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (r *recorder) Name() string { return "recorder" }

func (r *recorder) Update(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return r.err
}

func (r *recorder) received() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func closePublisher(t *testing.T, p *Publisher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestEventJSON(t *testing.T) {
	e := NewEvent(time.Unix(12345678, 0), []string{"Alloc", "Frees"}, "192.168.0.42")

	got, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ts":12345678,"metrics":["Alloc","Frees"],"ip_address":"192.168.0.42"}`
	if string(got) != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}

func TestPublisherNotifiesEveryObserverInOrder(t *testing.T) {
	p := NewPublisher(nil)
	first, second := &recorder{}, &recorder{}
	p.Register(first)
	p.Register(second)

	events := []Event{
		NewEvent(time.Unix(1, 0), []string{"Alloc"}, "10.0.0.1"),
		NewEvent(time.Unix(2, 0), []string{"PollCount"}, "10.0.0.2"),
	}
	for _, e := range events {
		p.Publish(context.Background(), e)
	}
	closePublisher(t, p)

	for _, o := range []*recorder{first, second} {
		if got := o.received(); !reflect.DeepEqual(got, events) {
			t.Errorf("received %+v, want %+v", got, events)
		}
	}
}

func TestPublisherReportsObserverFailure(t *testing.T) {
	var (
		mu       sync.Mutex
		reported []string
	)
	p := NewPublisher(func(observer string, err error) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, observer+": "+err.Error())
	})
	failing := &recorder{err: errors.New("boom")}
	healthy := &recorder{}
	p.Register(failing)
	p.Register(healthy)

	p.Publish(context.Background(), NewEvent(time.Now(), []string{"Alloc"}, "10.0.0.1"))
	closePublisher(t, p)

	if want := []string{"recorder: boom"}; !reflect.DeepEqual(reported, want) {
		t.Errorf("reported %q, want %q", reported, want)
	}
	if len(healthy.received()) != 1 {
		t.Error("a failing observer kept the event from the next one")
	}
}

func TestPublisherIgnoresEventsAfterClose(t *testing.T) {
	p := NewPublisher(nil)
	o := &recorder{}
	p.Register(o)
	closePublisher(t, p)

	p.Publish(context.Background(), NewEvent(time.Now(), []string{"Alloc"}, "10.0.0.1"))
	closePublisher(t, p)

	if got := o.received(); len(got) != 0 {
		t.Errorf("received %+v after Close, want nothing", got)
	}
}

func TestFileObserverAppendsLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	o, err := NewFileObserver(path)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		NewEvent(time.Unix(1, 0), []string{"Alloc", "Frees"}, "10.0.0.1"),
		NewEvent(time.Unix(2, 0), []string{"PollCount"}, "10.0.0.2"),
	}
	for _, e := range events {
		if err := o.Update(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() || scanner.Text() != "existing" {
		t.Fatalf("first line = %q, want the existing content kept", scanner.Text())
	}
	for i, want := range events {
		if !scanner.Scan() {
			t.Fatalf("line %d missing", i+2)
		}
		var got Event
		if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
			t.Fatalf("line %d: %v", i+2, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("line %d = %+v, want %+v", i+2, got, want)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if scanner.Scan() {
		t.Errorf("unexpected line %q", scanner.Text())
	}
}

func TestNewFileObserverFailsOnBadPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "audit.log")
	if _, err := NewFileObserver(path); err == nil {
		t.Error("NewFileObserver succeeded for a path in a missing directory")
	}
}

func TestHTTPObserverPostsEvent(t *testing.T) {
	var (
		method, contentType string
		got                 Event
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
	}))
	defer srv.Close()

	o, err := NewHTTPObserver(srv.URL+"/audit", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	want := NewEvent(time.Unix(12345678, 0), []string{"Alloc"}, "192.168.0.42")
	if err := o.Update(context.Background(), want); err != nil {
		t.Fatal(err)
	}

	if method != http.MethodPost {
		t.Errorf("method = %s, want POST", method)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("received %+v, want %+v", got, want)
	}
}

func TestHTTPObserverFailsOnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	o, err := NewHTTPObserver(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Update(context.Background(), NewEvent(time.Now(), []string{"Alloc"}, "10.0.0.1")); err == nil {
		t.Error("Update succeeded on a 500 answer")
	}
}

func TestNewHTTPObserverDefaultClient(t *testing.T) {
	o, err := NewHTTPObserver("http://example.com/audit", nil)
	if err != nil {
		t.Fatalf("NewHTTPObserver: %v", err)
	}
	if o.client == http.DefaultClient {
		t.Fatal("nil client resolved to http.DefaultClient")
	}
	if o.client.Timeout != DefaultDeliveryTimeout {
		t.Errorf("client timeout = %v, want %v", o.client.Timeout, DefaultDeliveryTimeout)
	}
	if o.client.Transport == nil || o.client.Transport == http.DefaultTransport {
		t.Error("client uses http.DefaultTransport")
	}
}

func TestNewHTTPObserverRejectsBadURL(t *testing.T) {
	for _, raw := range []string{"", "localhost:8080/audit", "ftp://example.com", "http://", "://bad"} {
		if _, err := NewHTTPObserver(raw, nil); err == nil {
			t.Errorf("NewHTTPObserver(%q) succeeded", raw)
		}
	}
}

func TestHTTPObserverKeepsSecretsOutOfNameAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close() // nothing listens any more, so the delivery fails

	u, err := url.Parse(addr)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("user", "s3cret-pass")
	u.Path = "/hook/s3cret-path"
	u.RawQuery = "token=s3cret-query"

	o, err := NewHTTPObserver(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = o.Update(context.Background(), Event{})
	if err == nil {
		t.Fatal("Update succeeded with no server listening")
	}

	for _, got := range []string{o.Name(), err.Error()} {
		if strings.Contains(got, "s3cret") || strings.Contains(got, "user") {
			t.Errorf("%q leaks a part of the URL", got)
		}
		if !strings.Contains(got, addr) {
			t.Errorf("%q does not name %s", got, addr)
		}
	}
}

func TestNewHTTPObserverKeepsSecretsOutOfValidationErrors(t *testing.T) {
	for _, raw := range []string{
		"http://user:s3cret/hook",                         // missing @: the password reads as a port
		"ftp://user:s3cret@example.com/hook?token=s3cret", // wrong scheme
		"http://user:s3cret@exa mple.com/",                // invalid host
		"http://example.com/s3cret\x7f",                   // control character
		"/hook/s3cret?token=s3cret",                       // relative
	} {
		_, err := NewHTTPObserver(raw, nil)
		if !errors.Is(err, ErrInvalidURL) {
			t.Errorf("NewHTTPObserver(%q) error = %v, want ErrInvalidURL", raw, err)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "user") {
			t.Errorf("NewHTTPObserver(%q) error %q leaks a part of the URL", raw, err)
		}
	}
}

// gatedObserver blocks every delivery until its gate is closed.
type gatedObserver struct{ gate chan struct{} }

func (o gatedObserver) Name() string { return "gated" }

func (o gatedObserver) Update(ctx context.Context, _ Event) error {
	select {
	case <-o.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestPublisherCallsOnErrorWithoutLock checks that the callback for a dropped
// event may use the publisher: run under the publisher's lock, the Register
// below would wait on that lock forever.
func TestPublisherCallsOnErrorWithoutLock(t *testing.T) {
	gate := make(chan struct{})

	var p *Publisher
	dropped := make(chan struct{}, 1)
	p = NewPublisher(func(observer string, err error) {
		if observer == "gated" && errors.Is(err, ErrQueueFull) {
			p.Register(gatedObserver{gate: gate})
			select {
			case dropped <- struct{}{}:
			default:
			}
		}
	})
	p.Register(gatedObserver{gate: gate})

	// One event is held by the blocked delivery, DefaultQueueSize fill the
	// queue and the last one has nowhere to go.
	notified := make(chan struct{})
	go func() {
		defer close(notified)
		for range DefaultQueueSize + 2 {
			p.Publish(context.Background(), Event{})
		}
	}()

	select {
	case <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish deadlocked in the OnError callback")
	}
	select {
	case <-dropped:
	default:
		t.Fatal("no event was reported as dropped")
	}

	close(gate)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestPublisherIsolatesSlowObserver checks that an observer blocked in its
// delivery neither delays the others nor keeps from them an event dropped for
// it alone.
func TestPublisherIsolatesSlowObserver(t *testing.T) {
	gate := make(chan struct{})

	var (
		mu      sync.Mutex
		dropped []string
	)
	p := NewPublisher(func(observer string, err error) {
		if errors.Is(err, ErrQueueFull) {
			mu.Lock()
			defer mu.Unlock()
			dropped = append(dropped, observer)
		}
	}, WithQueueSize(1))
	p.Register(gatedObserver{gate: gate})
	fast := &recorder{}
	p.Register(fast)

	// The blocked observer holds the first event and queues the second; the
	// third is dropped for it, but not for the fast one, which gets every
	// event while the other is still stuck.
	for i := range 3 {
		p.Publish(context.Background(), Event{TS: int64(i + 1)})
		waitFor(t, func() bool { return len(fast.received()) == i+1 })
	}

	close(gate)
	closePublisher(t, p)

	if want := []string{"gated"}; !reflect.DeepEqual(dropped, want) {
		t.Errorf("dropped for %q, want %q", dropped, want)
	}
}

// TestPublisherDeliveryTimeout checks that WithDeliveryTimeout bounds the
// delivery to an observer that never finishes on its own.
func TestPublisherDeliveryTimeout(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)

	failed := make(chan error, 1)
	p := NewPublisher(func(_ string, err error) { failed <- err }, WithDeliveryTimeout(10*time.Millisecond))
	p.Register(gatedObserver{gate: gate})
	p.Publish(context.Background(), Event{})

	select {
	case err := <-failed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the delivery was not cut off")
	}
	closePublisher(t, p)
}

// waitFor polls cond until it holds or a few seconds pass.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestPublisherRegisterAffectsLaterEventsOnly checks the boundary Register
// promises: an event published before an observer was registered never
// reaches it, even when it is still waiting in the queue at that moment.
func TestPublisherRegisterAffectsLaterEventsOnly(t *testing.T) {
	gate := make(chan struct{})
	p := NewPublisher(nil)
	p.Register(gatedObserver{gate: gate})

	// The first event holds up the delivery, so the second is still queued
	// when the recorder joins.
	p.Publish(context.Background(), Event{TS: 1})
	p.Publish(context.Background(), Event{TS: 2})

	late := &recorder{}
	p.Register(late)
	p.Publish(context.Background(), Event{TS: 3})

	close(gate)
	closePublisher(t, p)

	got := late.received()
	if len(got) != 1 || got[0].TS != 3 {
		t.Errorf("late observer received %+v, want only the event published after it joined", got)
	}
}
