package handler

import (
	"net"
	"net/http"
	"strings"
)

// TrustedSubnetMiddleware restricts metric writes to agent IPs in subnet.
// A nil subnet disables filtering. Other API operations remain available.
func TrustedSubnetMiddleware(subnet *net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := strings.TrimRight(r.URL.Path, "/")
			update := path == "/update" || path == "/updates" || strings.HasPrefix(path, "/update/")
			if subnet != nil && r.Method == http.MethodPost && update {
				ip := net.ParseIP(r.Header.Get("X-Real-IP"))
				if ip == nil || !subnet.Contains(ip) {
					http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
