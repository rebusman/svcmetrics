package agent

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
)

// statusError is the answer of a server that refused a batch. It carries the
// status code so that a refusal can be told apart from an outage.
type statusError struct {
	code   int
	status string
}

// Error returns the status line the server answered with.
func (e *statusError) Error() string {
	return fmt.Sprintf("unexpected status: %s", e.status)
}

// isRetriableSendError reports whether a failed report is worth repeating.
// Anything that happened on the wire is: the server may be restarting or the
// network may be briefly gone, and the batch itself is still valid. A server
// that answered and rejected the batch is repeated only when the answer says
// "not now" — a 4xx means the batch will be rejected again, however often it
// is offered.
//
// A payload that could not even be built is never retried: marshalling and
// compressing the same metrics fails the same way every time.
func isRetriableSendError(err error) bool {
	if err == nil {
		return false
	}

	var statusErr *statusError
	if errors.As(err, &statusErr) {
		return statusErr.code >= http.StatusInternalServerError ||
			statusErr.code == http.StatusTooManyRequests ||
			statusErr.code == http.StatusRequestTimeout
	}

	// *url.Error, which wraps every transport failure of http.Client, satisfies
	// net.Error, and so do the timeouts of the client itself.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED)
}
