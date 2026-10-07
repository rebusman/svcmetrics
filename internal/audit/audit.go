// Package audit records which metrics the server accepted, when and from whom.
//
// It follows the Observer pattern: a [Publisher] is the subject, and every
// receiver of the audit log — a file, a remote server — is an [Observer]
// registered with it. The handlers only know the publisher; adding another
// receiver means writing another observer, not touching the handlers.
//
// Delivery is asynchronous. [Publisher.Publish] queues the event and returns at
// once, so a slow or unreachable receiver never delays the request whose
// metrics are being audited. Every observer has a queue and a goroutine of its
// own, so a slow receiver does not delay the others either.
package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Event is one audit record: the metrics accepted by a single request.
type Event struct {
	// TS is the Unix time, in seconds, the metrics were accepted at.
	TS int64 `json:"ts"`
	// Metrics holds the names of the accepted metrics in the order they came.
	Metrics []string `json:"metrics"`
	// IPAddress is the address the request came from: the client itself, or
	// the reverse proxy in front of the server, whose forwarding headers are
	// not trusted.
	IPAddress string `json:"ip_address"`
}

// NewEvent builds an event for metrics accepted at t from ip.
func NewEvent(t time.Time, metrics []string, ip string) Event {
	return Event{TS: t.Unix(), Metrics: metrics, IPAddress: ip}
}

// Observer is a receiver of audit events.
type Observer interface {
	// Name identifies the receiver in error reports.
	Name() string
	// Update delivers one event. The context bounds the delivery.
	Update(ctx context.Context, e Event) error
}

// OnError observes an event that observer failed to receive, or one dropped
// for it because its queue was full. A typical implementation logs the
// failure: an audit problem must not fail the request, but it must not go
// unnoticed either.
type OnError func(observer string, err error)

// ErrQueueFull reports an event dropped because the receiver fell behind.
var ErrQueueFull = errors.New("audit queue is full")

const (
	// DefaultQueueSize is the number of events that may wait for delivery to
	// one observer before new ones are dropped for it.
	DefaultQueueSize = 1024

	// DefaultDeliveryTimeout bounds the delivery of one event to one observer.
	DefaultDeliveryTimeout = 5 * time.Second
)

// Option tunes the delivery policy of a [Publisher].
type Option func(*Publisher)

// WithQueueSize sets the number of events that may wait for delivery to one
// observer; the default is [DefaultQueueSize]. A size below 1 keeps the
// default.
func WithQueueSize(n int) Option {
	return func(p *Publisher) {
		if n > 0 {
			p.queueSize = n
		}
	}
}

// WithDeliveryTimeout sets the bound on the delivery of one event to one
// observer; the default is [DefaultDeliveryTimeout]. A duration of zero or
// less keeps the default. An [HTTPObserver] built without a client is also
// bounded by its client's timeout of DefaultDeliveryTimeout, so a longer bound
// needs a client passed to [NewHTTPObserver].
func WithDeliveryTimeout(d time.Duration) Option {
	return func(p *Publisher) {
		if d > 0 {
			p.deliveryTimeout = d
		}
	}
}

// Publisher is the subject of the audit: it fans every event out to all the
// registered observers. Each observer gets the events in the order they were
// published, from a queue and a goroutine of its own, started by
// [Publisher.Register] and stopped by [Publisher.Close]. The observers are
// independent: one that is slow or blocked fills its own queue and loses its
// own events, while the others keep receiving theirs.
type Publisher struct {
	mu      sync.RWMutex
	workers []*worker
	closed  bool

	wg        sync.WaitGroup
	done      chan struct{}
	closeOnce sync.Once

	queueSize       int
	deliveryTimeout time.Duration
	onError         OnError
}

// worker delivers the events queued for one observer.
type worker struct {
	observer Observer
	queue    chan Event
}

// NewPublisher creates a publisher with no observers and the delivery policy
// set by opts. onError may be nil.
func NewPublisher(onError OnError, opts ...Option) *Publisher {
	if onError == nil {
		onError = func(string, error) {}
	}
	p := &Publisher{
		done:            make(chan struct{}),
		queueSize:       DefaultQueueSize,
		deliveryTimeout: DefaultDeliveryTimeout,
		onError:         onError,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Register adds an observer that receives every event published from now on:
// an event already queued for the other observers does not reach it. An
// observer registered after [Publisher.Close] receives nothing.
func (p *Publisher) Register(o Observer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	w := &worker{observer: o, queue: make(chan Event, p.queueSize)}
	p.workers = append(p.workers, w)
	p.wg.Add(1)
	go p.deliver(w)
}

// Notify publishes the event for metrics accepted at t from ip. It adapts the
// publisher to the audit port of the handlers, which speaks in plain values.
func (p *Publisher) Notify(ctx context.Context, t time.Time, metrics []string, ip string) {
	p.Publish(ctx, NewEvent(t, metrics, ip))
}

// Publish queues an event for every observer. It never blocks: when the queue
// of an observer is full the event is dropped for that observer alone and
// reported through [OnError]. Events published after [Publisher.Close] are
// ignored.
//
// The lock only guards the sends, which must not race with Close closing the
// queues; [OnError] runs after it is released, so a callback is free to use
// the publisher and a slow one does not hold up Close.
func (p *Publisher) Publish(_ context.Context, e Event) {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return
	}
	var dropped []string
	for _, w := range p.workers {
		select {
		case w.queue <- e:
		default:
			dropped = append(dropped, w.observer.Name())
		}
	}
	p.mu.RUnlock()

	for _, name := range dropped {
		p.onError(name, ErrQueueFull)
	}
}

// Close stops accepting events and waits until the queued ones are delivered
// to every observer or ctx expires.
func (p *Publisher) Close(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		for _, w := range p.workers {
			close(w.queue)
		}
	}
	p.mu.Unlock()

	p.closeOnce.Do(func() {
		go func() {
			p.wg.Wait()
			close(p.done)
		}()
	})

	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("audit queue not drained: %w", ctx.Err())
	}
}

// deliver hands the events queued for w to its observer, one at a time, until
// the queue is closed and empty.
func (p *Publisher) deliver(w *worker) {
	defer p.wg.Done()
	for e := range w.queue {
		ctx, cancel := context.WithTimeout(context.Background(), p.deliveryTimeout)
		if err := w.observer.Update(ctx, e); err != nil {
			p.onError(w.observer.Name(), err)
		}
		cancel()
	}
}
