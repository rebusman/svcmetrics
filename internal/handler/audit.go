package handler

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/rebusman/svcmetrics/internal/audit"
)

// Auditor receives an event for every request whose metrics were stored. It is
// satisfied by [audit.Publisher]; a nil Auditor disables the audit.
type Auditor interface {
	Notify(ctx context.Context, e audit.Event)
}

// notifyAudit reports the metrics accepted by r to a, if the audit is enabled.
func notifyAudit(a Auditor, r *http.Request, metrics []string) {
	if a == nil || len(metrics) == 0 {
		return
	}
	a.Notify(r.Context(), audit.NewEvent(time.Now(), metrics, clientIP(r)))
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
