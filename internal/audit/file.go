package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// FileObserver appends every event to a file as one line of JSON.
type FileObserver struct {
	mu   sync.Mutex
	file *os.File
}

var _ Observer = (*FileObserver)(nil)

// NewFileObserver opens path for appending, creating the file when it does not
// exist. Opening it up front makes a wrong path fail at startup rather than
// with the first request.
func NewFileObserver(path string) (*FileObserver, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	return &FileObserver{file: f}, nil
}

// Name returns the path of the file.
func (o *FileObserver) Name() string { return "file " + o.file.Name() }

// Update appends the event on a line of its own. The line is written with a
// single call, so a concurrent writer to the same file cannot split it.
//
// The context is ignored: a write to a file cannot be cancelled. The file is
// expected to be local; one on a hung network file system blocks the
// [Publisher] and with it every other observer.
func (o *FileObserver) Update(_ context.Context, e Event) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	line = append(line, '\n')

	o.mu.Lock()
	defer o.mu.Unlock()
	if _, err := o.file.Write(line); err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

// Close closes the file.
func (o *FileObserver) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.file.Close()
}
