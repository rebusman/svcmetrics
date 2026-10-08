package handler

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Auditor is told about every request whose metrics were stored; a nil Auditor
// disables the audit. The port is declared here, in terms of plain values, so
// the handlers depend on no particular audit implementation.
type Auditor interface {
	// Notify reports the names of the metrics stored at t for a request that
	// came from ip. It must not block the request: delivery problems are the
	// auditor's to handle and never fail the request.
	Notify(ctx context.Context, t time.Time, metrics []string, ip string)
}

// notifyAudit reports the metrics accepted by r to a, if the audit is enabled.
func notifyAudit(a Auditor, r *http.Request, metrics []string) {
	if a == nil || len(metrics) == 0 {
		return
	}
	a.Notify(r.Context(), time.Now(), metrics, clientIP(r))
}

// clientIP returns the host part of the remote address of r, or the address as
// it is when it carries no port. Behind a reverse proxy that is the address of
// the proxy: X-Forwarded-For and X-Real-IP are ignored, since without a list of
// trusted proxies any client could put an arbitrary address there.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
