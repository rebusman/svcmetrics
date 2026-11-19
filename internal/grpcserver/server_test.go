package grpcserver

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestTrustedSubnetInterceptor(t *testing.T) {
	for _, tt := range []struct {
		name, subnet, peerAddr string
		allowed                bool
	}{
		{name: "disabled", allowed: true},
		{name: "missing peer", subnet: "192.0.2.0/24"},
		{name: "outside", subnet: "192.0.2.0/24", peerAddr: "198.51.100.1:1000"},
		{name: "inside", subnet: "192.0.2.0/24", peerAddr: "192.0.2.1:1000", allowed: true},
		{name: "ipv6", subnet: "2001:db8::/32", peerAddr: "[2001:db8::1]:1000", allowed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var subnet *net.IPNet
			if tt.subnet != "" {
				_, subnet, _ = net.ParseCIDR(tt.subnet)
			}
			ctx := context.Background()
			if tt.peerAddr != "" {
				addr, _ := net.ResolveTCPAddr("tcp", tt.peerAddr)
				ctx = peer.NewContext(ctx, &peer.Peer{Addr: addr})
			}
			called := false
			_, err := TrustedSubnetInterceptor(subnet)(ctx, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { called = true; return nil, nil })
			if called != tt.allowed {
				t.Fatalf("handler called = %v", called)
			}
			if tt.allowed && err != nil {
				t.Fatal(err)
			}
			if !tt.allowed && status.Code(err) != codes.PermissionDenied {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
