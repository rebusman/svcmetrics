// Package grpcserver implements the metrics gRPC API.
package grpcserver

import (
	"context"
	"crypto/rsa"
	"errors"
	"net"
	"time"

	"github.com/rebusman/svcmetrics/internal/grpccodec"
	"github.com/rebusman/svcmetrics/internal/handler"
	models "github.com/rebusman/svcmetrics/internal/model"
	pb "github.com/rebusman/svcmetrics/internal/proto"
	"github.com/rebusman/svcmetrics/internal/repository"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip" // registers gzip for compressed requests
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Options configures the server. Only Storage is required; every other field
// is the gRPC counterpart of the same feature of the HTTP router.
type Options struct {
	Storage repository.Writer
	// Subnet restricts the peers; nil allows all.
	Subnet *net.IPNet
	// Creds enables TLS; nil serves plaintext.
	Creds credentials.TransportCredentials
	// Key enables verification of request signatures and signing of
	// responses with HMAC-SHA256.
	Key string
	// PrivateKey enables decryption of encrypted requests.
	PrivateKey *rsa.PrivateKey
	// Auditor is told about the stored metrics; may be nil.
	Auditor handler.Auditor
	// Log enables one log line per call; may be nil.
	Log *logrus.Logger
	// Timeout bounds one call; zero means no limit.
	Timeout time.Duration
}

type service struct {
	pb.UnimplementedMetricsServer
	storage repository.Writer
	auditor handler.Auditor
}

// New registers the service with the interceptor chain in the order of the
// HTTP middleware: recovery, timeout, logging, trusted subnet, signature.
// Decryption happens in the codec, before the chain, and decompression in the
// gRPC transport.
func New(o Options) *grpc.Server {
	chain := []grpc.UnaryServerInterceptor{RecoveryInterceptor(), TimeoutInterceptor(o.Timeout)}
	if o.Log != nil {
		chain = append(chain, LoggingInterceptor(o.Log))
	}
	chain = append(chain, TrustedSubnetInterceptor(o.Subnet), HashInterceptor(o.Key))
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(chain...),
		grpc.ForceServerCodecV2(grpccodec.Codec{Private: o.PrivateKey}),
	}
	if o.Creds != nil {
		opts = append(opts, grpc.Creds(o.Creds))
	}
	server := grpc.NewServer(opts...)
	pb.RegisterMetricsServer(server, &service{storage: o.Storage, auditor: o.Auditor})
	return server
}

// TrustedSubnetInterceptor checks the transport peer address, which the
// client cannot forge, unlike request metadata. A nil subnet disables
// filtering. Behind a proxy the peer is the proxy, so the subnet must then
// cover it.
func TrustedSubnetInterceptor(subnet *net.IPNet) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if subnet != nil && !subnet.Contains(peerIP(ctx)) {
			return nil, status.Error(codes.PermissionDenied, "agent IP is outside trusted subnet")
		}
		return handler(ctx, req)
	}
}

func peerIP(ctx context.Context) net.IP {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return nil
	}
	if tcp, ok := p.Addr.(*net.TCPAddr); ok {
		return tcp.IP
	}
	host, _, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

func (s *service) UpdateMetrics(ctx context.Context, req *pb.UpdateMetricsRequest) (*pb.UpdateMetricsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "missing request")
	}
	metrics := make([]models.Metrics, 0, len(req.Metrics))
	for _, m := range req.Metrics {
		if m == nil || m.Id == "" {
			return nil, status.Error(codes.InvalidArgument, "missing metric ID")
		}
		metric := models.Metrics{ID: m.Id}
		switch m.Type {
		case pb.Metric_GAUGE:
			metric.MType = models.Gauge
			v := m.Value
			metric.Value = &v
		case pb.Metric_COUNTER:
			metric.MType = models.Counter
			d := m.Delta
			metric.Delta = &d
		default:
			return nil, status.Error(codes.InvalidArgument, "unknown metric type")
		}
		metrics = append(metrics, metric)
	}
	if err := s.storage.UpdateBatch(ctx, metrics); err != nil {
		switch {
		case errors.Is(err, models.ErrInvalidMetric):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		case errors.Is(err, context.Canceled):
			return nil, status.Error(codes.Canceled, err.Error())
		case errors.Is(err, context.DeadlineExceeded):
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		default:
			return nil, status.Error(codes.Internal, "failed to store metrics")
		}
	}
	if s.auditor != nil && len(metrics) > 0 {
		names := make([]string, len(metrics))
		for i, m := range metrics {
			names[i] = m.ID
		}
		ip := ""
		if addr := peerIP(ctx); addr != nil {
			ip = addr.String()
		}
		s.auditor.Notify(ctx, time.Now(), names, ip)
	}
	return &pb.UpdateMetricsResponse{}, nil
}
