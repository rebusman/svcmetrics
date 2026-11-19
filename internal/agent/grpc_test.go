package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/rebusman/svcmetrics/internal/grpcserver"
	"github.com/rebusman/svcmetrics/internal/hashing"
	pb "github.com/rebusman/svcmetrics/internal/proto"
	"github.com/rebusman/svcmetrics/internal/repository"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGRPCDelivery(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("127.0.0.0/8")
	storage := repository.NewMemStorage()
	server := grpcserver.New(grpcserver.Options{Storage: storage, Subnet: subnet})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	go func() { _ = server.Serve(listener) }()
	a := New("", 0, 0, 3, "", 1)
	conn, err := a.ConfigureGRPC(listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	a.CollectRuntimeMetrics()
	if err := a.SendMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	count, err := storage.GetCounter(context.Background(), "PollCount")
	if err != nil || count != 1 {
		t.Fatalf("counter = %d, err = %v", count, err)
	}
	gauges, err := storage.GetAllGauges(context.Background())
	if err != nil || len(gauges) == 0 {
		t.Fatalf("gauges = %v, err = %v", gauges, err)
	}
	client := pb.NewMetricsClient(conn)
	_, err = client.UpdateMetrics(context.Background(), &pb.UpdateMetricsRequest{Metrics: []*pb.Metric{
		{Id: "must-not-be-stored", Value: 1}, {Id: "bad", Type: pb.Metric_MType(99)},
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid batch: %v", err)
	}
	if _, err := storage.GetGauge(context.Background(), "must-not-be-stored"); err == nil {
		t.Fatal("invalid batch partially stored")
	}
}

func TestGRPCUntrustedPeer(t *testing.T) {
	_, subnet, _ := net.ParseCIDR("192.0.2.0/24")
	server := grpcserver.New(grpcserver.Options{Storage: repository.NewMemStorage(), Subnet: subnet})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	go func() { _ = server.Serve(listener) }()
	a := New("", 0, 0, 3, "", 1)
	conn, err := a.ConfigureGRPC(listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-real-ip", "192.0.2.1")
	_, err = pb.NewMetricsClient(conn).UpdateMetrics(ctx, &pb.UpdateMetricsRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("spoofed header must not help: %v", err)
	}
}

func TestGRPCTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	serverCreds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	clientCreds := credentials.NewClientTLSFromCert(pool, "")

	storage := repository.NewMemStorage()
	server := grpcserver.New(grpcserver.Options{Storage: storage, Creds: serverCreds})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	go func() { _ = server.Serve(listener) }()
	a := New("", 0, 0, 3, "", 1)
	conn, err := a.ConfigureGRPC(listener.Addr().String(), clientCreds)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	a.CollectRuntimeMetrics()
	if err := a.SendMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count, err := storage.GetCounter(context.Background(), "PollCount"); err != nil || count != 1 {
		t.Fatalf("counter = %d, err = %v", count, err)
	}
}

type recordingAuditor struct {
	mu    sync.Mutex
	names []string
	ip    string
}

func (r *recordingAuditor) Notify(_ context.Context, _ time.Time, metrics []string, ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, metrics...)
	r.ip = ip
}

func TestGRPCSecurityFeatures(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const key = "secret"
	storage := repository.NewMemStorage()
	auditor := &recordingAuditor{}
	server := grpcserver.New(grpcserver.Options{
		Storage: storage, Key: key, PrivateKey: private, Auditor: auditor,
		Log: logrus.New(), Timeout: time.Second,
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Stop()
	go func() { _ = server.Serve(listener) }()

	a := New("", 0, 0, 3, key, 1)
	a.SetPublicKey(&private.PublicKey)
	conn, err := a.ConfigureGRPC(listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	a.CollectRuntimeMetrics()
	if err := a.SendMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count, err := storage.GetCounter(context.Background(), "PollCount"); err != nil || count != 1 {
		t.Fatalf("counter = %d, err = %v", count, err)
	}
	auditor.mu.Lock()
	if len(auditor.names) == 0 || auditor.ip != "127.0.0.1" {
		t.Errorf("audit = %v from %q", auditor.names, auditor.ip)
	}
	auditor.mu.Unlock()

	// A wrong signature is rejected, an unsigned request is accepted, and an
	// encrypted request is refused by a server without a private key.
	plain := grpc.WithTransportCredentials(insecure.NewCredentials())
	raw, err := grpc.NewClient(listener.Addr().String(), plain)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	client := pb.NewMetricsClient(raw)
	bad := metadata.AppendToOutgoingContext(context.Background(), hashing.MetadataKey, "deadbeef")
	if _, err := client.UpdateMetrics(bad, &pb.UpdateMetricsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad signature: %v", err)
	}
	if _, err := client.UpdateMetrics(context.Background(), &pb.UpdateMetricsRequest{}); err != nil {
		t.Fatalf("unsigned request: %v", err)
	}
	var header metadata.MD
	if _, err := client.UpdateMetrics(context.Background(), &pb.UpdateMetricsRequest{}, grpc.Header(&header)); err != nil {
		t.Fatal(err)
	}
	if got := header.Get(hashing.MetadataKey); len(got) != 1 || !hashing.Equal(got[0], nil, key) {
		t.Errorf("response signature = %v", got)
	}

	noKey := grpcserver.New(grpcserver.Options{Storage: storage})
	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	defer noKey.Stop()
	go func() { _ = noKey.Serve(l2) }()
	b := New("", 0, 0, 3, "", 1)
	b.SetPublicKey(&private.PublicKey)
	c2, err := b.ConfigureGRPC(l2.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	b.CollectRuntimeMetrics()
	if err := b.SendMetrics(context.Background()); err == nil {
		t.Fatal("encrypted request accepted without a private key")
	}
}

func TestGRPCRetryCodes(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.ResourceExhausted, codes.DeadlineExceeded, codes.PermissionDenied, codes.InvalidArgument, codes.Canceled} {
		want := code == codes.Unavailable || code == codes.ResourceExhausted || code == codes.DeadlineExceeded
		if got := isRetriableSendError(status.Error(code, "test")); got != want {
			t.Errorf("%v: retry = %v", code, got)
		}
	}
}
