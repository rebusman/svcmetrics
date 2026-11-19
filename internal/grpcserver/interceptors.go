package grpcserver

import (
	"context"
	"time"

	"github.com/rebusman/svcmetrics/internal/grpccodec"
	"github.com/rebusman/svcmetrics/internal/hashing"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// RecoveryInterceptor turns a handler panic into the Internal status instead
// of crashing the server.
func RecoveryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// TimeoutInterceptor bounds the duration of a call; zero disables the limit.
func TimeoutInterceptor(d time.Duration) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if d > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, d)
			defer cancel()
		}
		return handler(ctx, req)
	}
}

// LoggingInterceptor logs one line per call with its method, status code,
// duration and peer.
func LoggingInterceptor(log *logrus.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		log.WithFields(logrus.Fields{
			"method":   info.FullMethod,
			"code":     status.Code(err).String(),
			"duration": time.Since(start),
			"peer":     peerIP(ctx),
		}).Info("gRPC call")
		return resp, err
	}
}

// HashInterceptor authenticates messages with HMAC-SHA256 under key, like
// the HTTP hash middleware: a request carrying a signature in the metadata is
// verified and a mismatch is rejected with InvalidArgument; an absent
// signature or [hashing.NoSignature] passes through. Successful responses are
// signed. The digest covers the deterministic protobuf encoding of the
// message, before compression and encryption. An empty key disables both.
func HashInterceptor(key string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if key == "" {
			return handler(ctx, req)
		}
		md, _ := metadata.FromIncomingContext(ctx)
		if sigs := md.Get(hashing.MetadataKey); len(sigs) > 0 && sigs[0] != hashing.NoSignature {
			m, ok := req.(proto.Message)
			if !ok {
				return nil, status.Error(codes.Internal, "request is not a protobuf message")
			}
			body, err := grpccodec.Marshal(m)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, "failed to read request")
			}
			if len(sigs) != 1 || !hashing.Equal(sigs[0], body, key) {
				return nil, status.Error(codes.InvalidArgument, "invalid request hash")
			}
		}
		resp, err := handler(ctx, req)
		if err != nil {
			return resp, err
		}
		m, ok := resp.(proto.Message)
		if !ok {
			return resp, nil
		}
		body, err := grpccodec.Marshal(m)
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to sign response")
		}
		if err := grpc.SetHeader(ctx, metadata.Pairs(hashing.MetadataKey, hashing.Sum(body, key))); err != nil {
			return nil, status.Error(codes.Internal, "failed to sign response")
		}
		return resp, nil
	}
}
