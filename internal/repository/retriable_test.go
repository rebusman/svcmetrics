package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgError builds the error a PostgreSQL server answers with for a SQLSTATE.
func pgError(code string) error {
	return &pgconn.PgError{Code: code, Message: "test"}
}

func TestIsRetriablePgError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "connection exception",
			err:  pgError(pgerrcode.ConnectionException),
			want: true,
		},
		{
			name: "connection failure",
			err:  pgError(pgerrcode.ConnectionFailure),
			want: true,
		},
		{
			name: "cannot connect now",
			err:  pgError(pgerrcode.CannotConnectNow),
			want: true,
		},
		{
			name: "admin shutdown",
			err:  pgError(pgerrcode.AdminShutdown),
			want: true,
		},
		{
			name: "deadlock detected",
			err:  pgError(pgerrcode.DeadlockDetected),
			want: true,
		},
		{
			name: "serialization failure",
			err:  pgError(pgerrcode.SerializationFailure),
			want: true,
		},
		{
			name: "too many connections",
			err:  pgError(pgerrcode.TooManyConnections),
			want: true,
		},
		{
			name: "wrapped connection exception",
			err:  fmt.Errorf("update gauge %q: %w", "Alloc", pgError(pgerrcode.ConnectionException)),
			want: true,
		},
		{
			name: "unique violation",
			err:  pgError(pgerrcode.UniqueViolation),
			want: false,
		},
		{
			name: "syntax error",
			err:  pgError(pgerrcode.SyntaxError),
			want: false,
		},
		{
			name: "undefined table",
			err:  pgError(pgerrcode.UndefinedTable),
			want: false,
		},
		{
			name: "query cancelled by the caller",
			err:  pgError(pgerrcode.QueryCanceled),
			want: false,
		},
		{
			name: "commit of unknown outcome",
			err:  &commitError{err: pgError(pgerrcode.ConnectionException)},
			want: false,
		},
		{
			name: "wrapped commit of unknown outcome",
			err:  fmt.Errorf("commit batch: %w", &commitError{err: pgError(pgerrcode.ConnectionFailure)}),
			want: false,
		},
		{
			name: "network failure",
			err:  &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
			want: true,
		},
		{
			name: "bad connection",
			err:  driver.ErrBadConn,
			want: true,
		},
		{
			name: "server closed the stream",
			err:  io.ErrUnexpectedEOF,
			want: true,
		},
		{
			name: "no rows",
			err:  sql.ErrNoRows,
			want: false,
		},
		{
			name: "cancelled context",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "expired context",
			err:  context.DeadlineExceeded,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetriablePgError(tt.err); got != tt.want {
				t.Fatalf("isRetriablePgError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRetriableFileError(t *testing.T) {
	var syntaxErr error
	if err := json.Unmarshal([]byte("{"), &struct{}{}); err != nil {
		syntaxErr = err
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{
			name: "missing file",
			err:  &fs.PathError{Op: "open", Path: "metrics.json", Err: fs.ErrNotExist},
			want: false,
		},
		{
			name: "forbidden file",
			err:  &fs.PathError{Op: "open", Path: "metrics.json", Err: fs.ErrPermission},
			want: false,
		},
		{
			name: "disk full",
			err:  &fs.PathError{Op: "write", Path: "metrics.json", Err: syscall.ENOSPC},
			want: false,
		},
		{
			name: "file held by another process",
			err:  &fs.PathError{Op: "rename", Path: "metrics.json", Err: syscall.EBUSY},
			want: true,
		},
		{
			name: "failed rename",
			err:  &os.LinkError{Op: "rename", Old: "tmp", New: "metrics.json", Err: syscall.EBUSY},
			want: true,
		},
		{
			name: "malformed snapshot",
			err:  syntaxErr,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetriableFileError(tt.err); got != tt.want {
				t.Fatalf("isRetriableFileError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestSaveFailsFastOnPermanentError verifies that a snapshot which cannot be
// written for a permanent reason is reported at once: the periodic saver must
// not sit through the retry schedule every time.
func TestSaveFailsFastOnPermanentError(t *testing.T) {
	s := NewMemStorage()
	if _, err := s.UpdateGauge(context.Background(), "Alloc", 1.5); err != nil {
		t.Fatalf("UpdateGauge() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "missing-dir", "metrics.json")

	start := time.Now()
	err := s.Save(path)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Save() error = nil, want a failure")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Save() error = %v, want a missing directory", err)
	}
	if elapsed > time.Second {
		t.Fatalf("Save() took %v, want no retry of a permanent failure", elapsed)
	}
}

// TestLoadFailsFastOnMalformedSnapshot verifies that a corrupted file is
// reported straight away rather than parsed four times.
func TestLoadFailsFastOnMalformedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	s := NewMemStorage()

	start := time.Now()
	err := s.Load(path)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Load() error = nil, want a failure")
	}
	if elapsed > time.Second {
		t.Fatalf("Load() took %v, want no retry of a malformed snapshot", elapsed)
	}
}
