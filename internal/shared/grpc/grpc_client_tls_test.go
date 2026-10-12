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
	"fmt"
	"github.com/arcentrix/arcentra/pkg/safe"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

func tlsFixture(t *testing.T, expired bool) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"controller.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if expired {
		leaf.NotAfter = now.Add(-time.Minute)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{leafDER, der}, PrivateKey: key}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := ggrpc.NewServer(ggrpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})))
	healthv1.RegisterHealthServer(server, health.NewServer())
	// 测试服务由测试清理器停止
	safe.Go(func() { _ = server.Serve(listener) })
	t.Cleanup(server.Stop)
	caFile := filepath.Join(t.TempDir(), "synthetic-ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return listener.Addr().String(), caFile
}

func TestClientStrictControllerTLS(t *testing.T) {
	for _, tc := range []struct {
		name               string
		expired, untrusted bool
		nameOverride       string
		want               bool
	}{
		{name: "trusted", nameOverride: "controller.test", want: true},
		{name: "hostname mismatch", nameOverride: "other.test"},
		{name: "expired", expired: true, nameOverride: "controller.test"},
		{name: "untrusted self signed", untrusted: true, nameOverride: "controller.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, ca := tlsFixture(t, tc.expired)
			if tc.untrusted {
				ca = ""
			}
			client, err := NewGrpcClient(ClientConf{ServerAddr: addr, TLS: ClientTLSConfig{Enabled: true, CAFile: ca, ServerName: tc.nameOverride}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
			defer cancel()
			_, err = healthv1.NewHealthClient(client.GetConn()).Check(ctx, &healthv1.HealthCheckRequest{})
			if (err == nil) != tc.want {
				t.Fatalf("verified RPC=%v want=%v", err == nil, tc.want)
			}
			if !client.ControllerAuthenticated() {
				t.Fatal("TLS client lost authenticated transport policy")
			}
			if tc.want {
				if err := client.reconnect(); err != nil {
					t.Fatal(err)
				}
				_, err = healthv1.NewHealthClient(client.GetConn()).Check(ctx, &healthv1.HealthCheckRequest{})
				if err != nil {
					t.Fatal(err)
				}
			}
			_ = client.Close()
			if client.ControllerAuthenticated() {
				t.Fatal("closed client marked authenticated")
			}
		})
	}
}

func TestClientTLSConfigurationFailsClosed(t *testing.T) {
	for _, cfg := range []ClientTLSConfig{{CAFile: "unused"}, {ServerName: "controller.test"}, {Enabled: true, ServerName: " invalid "}, {Enabled: true, CAFile: filepath.Join(t.TempDir(), "missing")}} {
		if _, err := NewGrpcClient(ClientConf{ServerAddr: "127.0.0.1:1", TLS: cfg}); err == nil {
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
	client, err := NewGrpcClient(ClientConf{ServerAddr: "127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.ControllerAuthenticated() {
		t.Fatal("plaintext trusted")
	}
	if err := client.Start(ClientConf{ServerAddr: "127.0.0.1:1", TLS: ClientTLSConfig{Enabled: true}}); err == nil {
		t.Fatal("allowed transport reconfiguration")
	}
	if client.ControllerAuthenticated() {
		t.Fatal("failed Start changed trust")
	}
}

func TestControllerIdentityBindsLoadedTrustAndEndpoint(t *testing.T) {
	address, caFile := tlsFixture(t, false)
	cfg := ClientConf{ServerAddr: address, TLS: ClientTLSConfig{Enabled: true, CAFile: caFile, ServerName: "controller.test"}}
	first, err := NewGrpcClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	binding, err := first.ControllerIdentityBinding()
	if err != nil || len(binding) != 64 {
		t.Fatalf("binding unavailable: %v", err)
	}
	second, err := NewGrpcClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	same, err := second.ControllerIdentityBinding()
	if err != nil || same != binding {
		t.Fatal("same transport identity changed")
	}
	_, differentCA := tlsFixture(t, false)
	replacement, err := os.ReadFile(differentCA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	stable, err := first.ControllerIdentityBinding()
	if err != nil || stable != binding {
		t.Fatal("loaded identity followed mutable CA file")
	}
	changed, err := NewGrpcClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer changed.Close()
	changedBinding, err := changed.ControllerIdentityBinding()
	if err != nil || changedBinding == binding {
		t.Fatal("changed loaded trust was not bound")
	}
	cfg.TLS.CAFile = differentCA
	cfg.TLS.ServerName = "other.test"
	renamed, err := NewGrpcClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer renamed.Close()
	renamedBinding, err := renamed.ControllerIdentityBinding()
	if err != nil || renamedBinding == changedBinding {
		t.Fatal("verified server name not bound")
	}
	cfg.TLS.ServerName = "controller.test"
	cfg.ServerAddr = "127.0.0.1:1"
	moved, err := NewGrpcClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Close()
	movedBinding, err := moved.ControllerIdentityBinding()
	if err != nil || movedBinding == changedBinding {
		t.Fatal("endpoint not bound")
	}
}

func TestControllerIdentityRequiresExplicitAuthenticatedTrust(t *testing.T) {
	for _, cfg := range []ClientConf{{ServerAddr: "127.0.0.1:1"}, {ServerAddr: "127.0.0.1:1", TLS: ClientTLSConfig{Enabled: true}}} {
		client, err := NewGrpcClient(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.ControllerIdentityBinding(); err == nil {
			t.Fatal("unbound trust allowed for credential persistence")
		}
		_ = client.Close()
	}
	var client *ClientWrapper
	if _, err := client.ControllerIdentityBinding(); err == nil {
		t.Fatal("nil client accepted")
	}
}

func TestReconnectKeepsPublishedClientsStable(t *testing.T) {
	address, caFile := tlsFixture(t, false)
	client, err := NewGrpcClient(ClientConf{ServerAddr: address, TLS: ClientTLSConfig{Enabled: true, CAFile: caFile, ServerName: "controller.test"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	originalConn, originalAgent := client.GetConn(), client.AgentClient
	results := make(chan error, 2)
	safe.Go(func() {
		for i := 0; i < 25; i++ {
			if err := client.reconnect(); err != nil {
				results <- err
				return
			}
		}
		results <- nil
	})
	safe.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for i := 0; i < 25; i++ {
			if client.AgentClient != originalAgent {
				results <- fmt.Errorf("published Agent client replaced")
				return
			}
			if _, err := healthv1.NewHealthClient(client.GetConn()).Check(ctx, &healthv1.HealthCheckRequest{}); err != nil {
				results <- err
				return
			}
		}
		results <- nil
	})
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if client.GetConn() != originalConn || client.AgentClient != originalAgent {
		t.Fatal("reconnect changed published clients")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.reconnect(); err == nil {
		t.Fatal("closed client resurrected")
	}
	if client.GetConn() != nil || client.ControllerAuthenticated() {
		t.Fatal("closed client regained transport")
	}
}
