package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/pkg/safe"
	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func serverTLSFixture(t *testing.T, change func(*x509.Certificate)) (ServerTLSConfig, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic server test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	root, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"controller.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Minute), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if change != nil {
		change(leaf)
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := ServerTLSConfig{Enabled: true, CertFile: filepath.Join(dir, "synthetic-cert.pem"), KeyFile: filepath.Join(dir, "synthetic-key.pem")}
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root})
	certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), rootPEM...)
	if err := os.WriteFile(cfg.CertFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.KeyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		t.Fatal("synthetic CA could not be parsed")
	}
	return cfg, pool
}

func newTestServer(t *testing.T, cfg Conf) *ServerWrapper {
	t.Helper()
	s, err := NewGrpcServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
	return s
}

type syntheticRegistrationService struct {
	agentv1.UnimplementedAgentServiceServer
	entered chan struct{}
	release chan struct{}
}

func (s *syntheticRegistrationService) Register(ctx context.Context, _ *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	if s.entered != nil {
		close(s.entered)
	}
	if s.release != nil {
		// 故意忽略取消，验证不合作处理器不会阻塞有界停机
		<-s.release
	}
	return &agentv1.RegisterResponse{}, nil
}

func TestServerTLSVerifiedRPCAndAuthentication(t *testing.T) {
	cfg, roots := serverTLSFixture(t, nil)
	s := newTestServer(t, Conf{Host: "127.0.0.1", TLS: cfg})
	// 已加载的凭证不跟随磁盘变更，重新读取或轮换必须重启生命周期
	if err := os.Remove(cfg.CertFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.KeyFile); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = false
	agentv1.RegisterAgentServiceServer(s.svr, &syntheticRegistrationService{})
	if s.Ready() {
		t.Fatal("server ready before binding")
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.Ready() {
		t.Fatal("bound server not ready")
	}
	client, err := ggrpc.NewClient(s.listener.Addr().String(), ggrpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "controller.test", MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := agentv1.NewAgentServiceClient(client).Register(ctx, &agentv1.RegisterRequest{}); err != nil {
		t.Fatalf("verified RPC failed: %v", err)
	}
	if _, err := agentv1.NewAgentServiceClient(client).Heartbeat(ctx, &agentv1.HeartbeatRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("TLS bypassed per-Agent authentication: %v", err)
	}
}

func TestServerTLSRefusesDowngradeAndWrongIdentity(t *testing.T) {
	cfg, roots := serverTLSFixture(t, nil)
	_, otherRoots := serverTLSFixture(t, nil)
	s := newTestServer(t, Conf{Host: "127.0.0.1", TLS: cfg})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		config *tls.Config
		want   bool
	}{
		{"TLS12", &tls.Config{RootCAs: roots, ServerName: "controller.test", MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}, true},
		{"TLS13", &tls.Config{RootCAs: roots, ServerName: "controller.test", MinVersion: tls.VersionTLS13}, true},
		{"TLS11 rejected", &tls.Config{RootCAs: roots, ServerName: "controller.test", MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}, false},
		{"hostname mismatch", &tls.Config{RootCAs: roots, ServerName: "wrong.test", MinVersion: tls.VersionTLS12}, false},
		{"untrusted root", &tls.Config{RootCAs: otherRoots, ServerName: "controller.test", MinVersion: tls.VersionTLS12}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := (&tls.Dialer{Config: tc.config}).DialContext(ctx, "tcp", s.listener.Addr().String())
			if conn != nil {
				_ = conn.Close()
			}
			if (err == nil) != tc.want {
				t.Fatalf("TLS result %v, want success %v", err, tc.want)
			}
		})
	}
	client, err := ggrpc.NewClient(s.listener.Addr().String(), ggrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = agentv1.NewAgentServiceClient(client).Register(ctx, &agentv1.RegisterRequest{})
	if err == nil || status.Code(err) == codes.Unimplemented || status.Code(err) == codes.Unauthenticated {
		t.Fatalf("plaintext reached TLS RPC dispatch: %v", err)
	}
}

func TestServerTLSConfigurationFailsClosed(t *testing.T) {
	valid, _ := serverTLSFixture(t, nil)
	mismatch, _ := serverTLSFixture(t, nil)
	expired, _ := serverTLSFixture(t, func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Second) })
	future, _ := serverTLSFixture(t, func(c *x509.Certificate) { c.NotBefore = time.Now().Add(30 * time.Second) })
	clientOnly, _ := serverTLSFixture(t, func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} })
	malformed := filepath.Join(t.TempDir(), "malformed.pem")
	if err := os.WriteFile(malformed, []byte("synthetic malformed key material"), 0600); err != nil {
		t.Fatal(err)
	}
	oversize := filepath.Join(t.TempDir(), "oversize.pem")
	if err := os.WriteFile(oversize, []byte(strings.Repeat("X", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  ServerTLSConfig
	}{
		{"disabled with certificate", ServerTLSConfig{CertFile: valid.CertFile}},
		{"disabled with key", ServerTLSConfig{KeyFile: valid.KeyFile}},
		{"missing both", ServerTLSConfig{Enabled: true}},
		{"missing cert", ServerTLSConfig{Enabled: true, KeyFile: valid.KeyFile}},
		{"missing key", ServerTLSConfig{Enabled: true, CertFile: valid.CertFile}},
		{"unreadable cert", ServerTLSConfig{Enabled: true, CertFile: "/synthetic/absent-cert.pem", KeyFile: valid.KeyFile}},
		{"unreadable key", ServerTLSConfig{Enabled: true, CertFile: valid.CertFile, KeyFile: "/synthetic/absent-key.pem"}},
		{"malformed cert", ServerTLSConfig{Enabled: true, CertFile: malformed, KeyFile: valid.KeyFile}},
		{"malformed key", ServerTLSConfig{Enabled: true, CertFile: valid.CertFile, KeyFile: malformed}},
		{"oversized cert", ServerTLSConfig{Enabled: true, CertFile: oversize, KeyFile: valid.KeyFile}},
		{"directory key", ServerTLSConfig{Enabled: true, CertFile: valid.CertFile, KeyFile: t.TempDir()}},
		{"mismatched key", ServerTLSConfig{Enabled: true, CertFile: valid.CertFile, KeyFile: mismatch.KeyFile}},
		{"expired", expired}, {"not yet valid", future}, {"wrong usage", clientOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewGrpcServer(Conf{TLS: tc.cfg})
			if err == nil || s != nil {
				t.Fatal("invalid TLS config created a usable server")
			}
			if strings.Contains(err.Error(), tc.cfg.CertFile) && tc.cfg.CertFile != "" {
				t.Fatal("certificate path exposed")
			}
		})
	}
	t.Setenv("GODEBUG", "x509keypairleaf=0")
	s := newTestServer(t, Conf{TLS: valid})
	if s == nil {
		t.Fatal("valid PEM rejected with compatibility setting")
	}
}

func TestServerPlaintextAndReflectionAreExplicit(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := newTestServer(t, Conf{Host: "127.0.0.1", Reflection: enabled, TLS: ServerTLSConfig{Enabled: false}})
		_, reflected := s.svr.GetServiceInfo()["grpc.reflection.v1.ServerReflection"]
		if reflected != enabled {
			t.Fatal("reflection setting ignored")
		}
		agentv1.RegisterAgentServiceServer(s.svr, &syntheticRegistrationService{})
		if err := s.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		client, err := ggrpc.NewClient(s.listener.Addr().String(), ggrpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = agentv1.NewAgentServiceClient(client).Register(ctx, &agentv1.RegisterRequest{})
		cancel()
		_ = client.Close()
		if err != nil {
			t.Fatalf("explicit legacy plaintext failed: %v", err)
		}
	}
}

func TestServerBindFailureAndCanceledStartup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s := newTestServer(t, Conf{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port})
	if err := s.Start(context.Background()); err == nil || s.Ready() {
		t.Fatal("occupied port reported ready")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Start(ctx); !errors.Is(err, context.Canceled) || s.Ready() {
		t.Fatal("canceled startup reported ready")
	}
}

func TestServerServeFailureAndSingleLifecycle(t *testing.T) {
	s := newTestServer(t, Conf{Host: "127.0.0.1"})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("second start accepted")
	}
	_ = s.listener.Close()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("serve failure not propagated")
	}
	if s.Ready() || s.Err() == nil {
		t.Fatal("failed listener remains ready or hid its failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err == nil {
		t.Fatal("stopped server restarted")
	}
}

func TestServerShutdownBoundsUncooperativeRPC(t *testing.T) {
	s := newTestServer(t, Conf{Host: "127.0.0.1"})
	service := &syntheticRegistrationService{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(service.release)
	agentv1.RegisterAgentServiceServer(s.svr, service)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, err := ggrpc.NewClient(s.listener.Addr().String(), ggrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rpcCancel()
	rpcDone := make(chan error, 1)
	safe.Go(func() {
		_, err := agentv1.NewAgentServiceClient(client).Register(rpcCtx, &agentv1.RegisterRequest{})
		rpcDone <- err
	})
	select {
	case <-service.entered:
	case <-time.After(time.Second):
		t.Fatal("RPC did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked RPC did not force shutdown: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond || s.Ready() {
		t.Fatal("shutdown exceeded bound or remained ready")
	}
	select {
	case err := <-rpcDone:
		if err == nil {
			t.Fatal("blocked RPC unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("forced stop did not close RPC connection")
	}
}

func TestServerStopBeforeStartAndInvalidConfiguration(t *testing.T) {
	s := newTestServer(t, Conf{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("never-started server was not closed")
	}
	if err := s.Start(ctx); err == nil {
		t.Fatal("closed server started")
	}
	for _, cfg := range []Conf{{Port: -1}, {Port: 65536}, {MaxConnections: -1}, {ReadWriteTimeout: -1}} {
		if _, err := NewGrpcServer(cfg); err == nil {
			t.Fatal("invalid listener config accepted")
		}
	}
}
