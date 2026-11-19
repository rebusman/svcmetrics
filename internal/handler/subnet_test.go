package handler

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrustedSubnetMiddleware(t *testing.T) {
	for _, tc := range []struct {
		name, cidr, ip, method, path string
		want                         int
	}{
		{"allowed", "192.168.1.0/24", "192.168.1.42", "POST", "/updates/", 204},
		{"outside", "192.168.1.0/24", "192.168.2.1", "POST", "/updates", 403},
		{"missing", "192.168.1.0/24", "", "POST", "/update", 403},
		{"invalid", "192.168.1.0/24", "invalid", "POST", "/update/gauge/A/1", 403},
		{"port", "192.168.1.0/24", "192.168.1.1:80", "POST", "/updates/", 403},
		{"ipv6", "2001:db8::/32", "2001:db8::1", "POST", "/update", 204},
		{"ipv6 outside", "2001:db8::/32", "2001:db9::1", "POST", "/update", 403},
		{"disabled", "", "", "POST", "/updates/", 204},
		{"read", "192.168.1.0/24", "", "GET", "/", 204},
		{"value", "192.168.1.0/24", "", "POST", "/value", 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var subnet *net.IPNet
			if tc.cidr != "" {
				_, subnet, _ = net.ParseCIDR(tc.cidr)
			}
			called := false
			h := TrustedSubnetMiddleware(subnet)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("X-Real-IP", tc.ip)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want || called != (tc.want == 204) {
				t.Fatalf("status=%d, handler called=%v; want status=%d", w.Code, called, tc.want)
			}
		})
	}
}
