// Package audit records which metrics the server accepted, when and from whom.
//
// It follows the Observer pattern: a [Publisher] is the subject, and every
// receiver of the audit log — a file, a remote server — is an [Observer]
// registered with it. The handlers only know the publisher; adding another
// receiver means writing another observer, not touching the handlers.
//
// Delivery is asynchronous. [Publisher.Notify] queues the event and returns at
// once, so a slow or unreachable receiver never delays the request whose
// metrics are being audited.
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

// OnError observes an event that an observer failed to receive, or one dropped
// because the queue was full, in which case observer is empty. A typical
// implementation logs the failure: an audit problem must not fail the request,
// but it must not go unnoticed either.
type OnError func(observer string, err error)

// ErrQueueFull reports an event dropped because the receivers fell behind.
var ErrQueueFull = errors.New("audit queue is full")

const (
	// queueSize is the number of events that may wait for delivery before new
	// ones are dropped.
	queueSize = 1024

	// deliveryTimeout bounds the delivery of one event to one observer.
	deliveryTimeout = 5 * time.Second
)

// Publisher is the subject of the audit: it fans every event out to all the
// registered observers. Events are delivered one at a time, in the order they
// were published, by a single background goroutine started by
// [NewPublisher] and stopped by [Publisher.Close]. The observers are therefore
// coupled: one that blocks past its context stalls the delivery to the others.
type Publisher struct {
	mu        sync.RWMutex
	observers []Observer
	closed    bool

	queue   chan Event
	done    chan struct{}
	onError OnError
}

// NewPublisher starts a publisher with no observers. onError may be nil.
func NewPublisher(onError OnError) *Publisher {
	if onError == nil {
		onError = func(string, error) {}
	}
	p := &Publisher{
		queue:   make(chan Event, queueSize),
		done:    make(chan struct{}),
		onError: onError,
	}
	go p.run()
	return p
}

// Register adds an observer that receives every event published from now on.
func (p *Publisher) Register(o Observer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observers = append(p.observers, o)
}

// Notify queues an event for every observer. It never blocks: when the queue is
// full the event is dropped and reported through [OnError]. Events published
// after [Publisher.Close] are ignored.
func (p *Publisher) Notify(_ context.Context, e Event) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return
	}
	select {
	case p.queue <- e:
	default:
		p.onError("", ErrQueueFull)
	}
}

// Close stops accepting events and waits until the queued ones are delivered
// or ctx expires.
func (p *Publisher) Close(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()

	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("audit queue not drained: %w", ctx.Err())
	}
}

func (p *Publisher) run() {
	defer close(p.done)
	for e := range p.queue {
		p.mu.RLock()
		observers := p.observers
		p.mu.RUnlock()

		for _, o := range observers {
			ctx, cancel := context.WithTimeout(context.Background(), deliveryTimeout)
			if err := o.Update(ctx, e); err != nil {
				p.onError(o.Name(), err)
			}
			cancel()
		}
	}
}
