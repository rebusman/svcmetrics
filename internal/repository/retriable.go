package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"syscall"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// isRetriablePgError reports whether a failed database call is worth repeating.
// A server that answered with a SQLSTATE is classified by that code; a failure
// that never got an answer is classified by the transport error underneath,
// since a connection that broke on the way tells us nothing about the query.
//
// A cancelled context is never retried: the caller is already gone.
func isRetriablePgError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// A commit of unknown outcome must not be replayed, whatever broke it.
	var commitErr *commitError
	if errors.As(err, &commitErr) {
		return false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return isRetriablePgCode(pgErr.Code)
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// database/sql reports a connection it can no longer use as ErrBadConn, and
	// a server that closed mid-query surfaces as an unexpected end of stream.
	return errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED)
}

// isRetriablePgCode reports whether a SQLSTATE describes a passing condition.
// Everything else — a syntax error, a constraint violation, a missing table —
// will fail exactly the same way on the next attempt.
func isRetriablePgCode(code string) bool {
	switch {
	// Class 08: the connection was lost or could not be established.
	case pgerrcode.IsConnectionException(code):
		return true
	// Class 53: the server is out of connections, memory or disk for now.
	case pgerrcode.IsInsufficientResources(code):
		return true
	}

	switch code {
	// Class 40: two concurrent batches got in each other's way. Our batches are
	// atomic, so repeating the whole transaction is safe.
	case pgerrcode.SerializationFailure, pgerrcode.DeadlockDetected:
		return true
	// Class 57: the server is restarting or not accepting connections yet. The
	// remaining codes of that class — a cancelled query, a dropped database —
	// are deliberately left out.
	case pgerrcode.AdminShutdown, pgerrcode.CrashShutdown, pgerrcode.CannotConnectNow:
		return true
	}

	return false
}

// isRetriableFileError reports whether a failed snapshot read or write may
// succeed on a second try. A file operation that failed because the path is
// wrong, forbidden, a directory, on a read-only mount or out of space is
// permanent, and a malformed snapshot is not a file error at all; the rest — a
// file another process holds open, a device that is busy — usually clears
// within seconds.
func isRetriableFileError(err error) bool {
	if err == nil {
		return false
	}

	// The permanent failures are matched by their portable errno rather than by
	// the message, which differs between the platforms: Windows synthesises the
	// same values for its own error codes, so EISDIR here covers both "is a
	// directory" on Unix and ERROR_DIRECTORY on Windows.
	switch {
	case errors.Is(err, fs.ErrNotExist),
		errors.Is(err, fs.ErrPermission),
		errors.Is(err, fs.ErrInvalid),
		errors.Is(err, syscall.ENOSPC),
		errors.Is(err, syscall.EISDIR),
		errors.Is(err, syscall.ENOTDIR),
		errors.Is(err, syscall.EROFS):
		return false
	}

	var (
		pathErr *fs.PathError
		linkErr *os.LinkError
	)
	return errors.As(err, &pathErr) || errors.As(err, &linkErr)
}
