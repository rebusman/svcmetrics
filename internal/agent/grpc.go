package agent

import (
	"context"
	"fmt"

	"github.com/rebusman/svcmetrics/internal/grpccodec"
	"github.com/rebusman/svcmetrics/internal/hashing"
	models "github.com/rebusman/svcmetrics/internal/model"
	pb "github.com/rebusman/svcmetrics/internal/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/metadata"
)

// ConfigureGRPC selects gRPC delivery. Call after SetPublicKey and before
// reporting starts, and close the returned connection after Run returns.
// Address must be host:port; a nil creds means plaintext transport. Like the
// HTTP delivery, the batches are gzip-compressed, encrypted when a public key
// is set and signed when the agent has a key.
func (a *Agent) ConfigureGRPC(address string, creds credentials.TransportCredentials) (*grpc.ClientConn, error) {
	if creds == nil {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(
			grpc.ForceCodecV2(grpccodec.Codec{Public: a.publicKey}),
			grpc.UseCompressor(gzip.Name),
		),
	)
	if err != nil {
		return nil, err
	}
	key := a.key
	client := pb.NewMetricsClient(conn)
	a.grpcSend = func(ctx context.Context, metrics []models.Metrics) error {
		req := &pb.UpdateMetricsRequest{Metrics: make([]*pb.Metric, 0, len(metrics))}
		for _, m := range metrics {
			metric := &pb.Metric{Id: m.ID}
			switch m.MType {
			case models.Counter:
				metric.Type = pb.Metric_COUNTER
				if m.Delta != nil {
					metric.Delta = *m.Delta
				}
			case models.Gauge:
				metric.Type = pb.Metric_GAUGE
				if m.Value != nil {
					metric.Value = *m.Value
				}
			default:
				return fmt.Errorf("unknown metric type %q for %q", m.MType, m.ID)
			}
			req.Metrics = append(req.Metrics, metric)
		}
		ctx, cancel := context.WithTimeout(ctx, clientTimeout)
		defer cancel()
		if key != "" {
			body, err := grpccodec.Marshal(req)
			if err != nil {
				return err
			}
			ctx = metadata.AppendToOutgoingContext(ctx, hashing.MetadataKey, hashing.Sum(body, key))
		}
		_, err := client.UpdateMetrics(ctx, req)
		return err
	}
	return conn, nil
}
