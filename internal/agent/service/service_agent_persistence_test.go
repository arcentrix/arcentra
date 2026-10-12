package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/agent/config"
	"github.com/arcentrix/arcentra/internal/agent/identitystore"
	agentgrpc "github.com/arcentrix/arcentra/internal/shared/grpc"
	"github.com/arcentrix/arcentra/pkg/cron"
	"github.com/arcentrix/arcentra/pkg/safe"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type persistenceRPC struct {
	agentv1.AgentServiceClient
	register  func(context.Context, *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error)
	heartbeat func(context.Context, *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error)
	calls     int
}

func (c *persistenceRPC) Register(ctx context.Context, req *agentv1.RegisterRequest, _ ...grpc.CallOption) (*agentv1.RegisterResponse, error) {
	c.calls++
	return c.register(ctx, req)
}
func (c *persistenceRPC) Heartbeat(ctx context.Context, req *agentv1.HeartbeatRequest, _ ...grpc.CallOption) (*agentv1.HeartbeatResponse, error) {
	return c.heartbeat(ctx, req)
}
func syntheticTLSClient(t *testing.T, rpc agentv1.AgentServiceClient) *agentgrpc.ClientWrapper {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic-controller"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "synthetic-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := agentgrpc.NewGrpcClient(agentgrpc.ClientConf{ServerAddr: "synthetic-controller.test:9443", TLS: agentgrpc.ClientTLSConfig{Enabled: true, CAFile: ca, ServerName: "synthetic-controller.test"}})
	if err != nil {
		t.Fatal(err)
	}
	client.AgentClient = rpc
	t.Cleanup(func() { _ = client.Close() })
	return client
}
func persistenceConf(t *testing.T) *config.AgentConfig {
	t.Helper()
	base := t.TempDir()
	return &config.AgentConfig{Agent: config.AgentInfo{WorkspaceDir: filepath.Join(base, "workspace"), IdentityPersistence: config.IdentityPersistenceConfig{Enabled: true, Directory: filepath.Join(base, "identity")}}, Grpc: config.GrpcConfig{RegistrationToken: "art_synthetic_bootstrap_secret", ServerAddr: "synthetic-controller.test:9443"}}
}
func closeIdentity(t *testing.T, s *AgentServiceImpl) {
	t.Helper()
	if err := s.CloseIdentity(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentRegistrationRestoresBeforeTerminalGuard(t *testing.T) {
	rpc := &restartRegistrationClient{}
	client := syntheticTLSClient(t, rpc)
	first := persistenceConf(t)
	svc := NewAgentServiceImpl(first, client, nil)
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	closeIdentity(t, svc)
	// An existing V7 terminal journal must not prevent restoring the same identity.
	if err := os.MkdirAll(filepath.Join(first.Agent.WorkspaceDir, ".terminal-report-outbox"), 0700); err != nil {
		t.Fatal(err)
	}
	restarted := *first
	restarted.Agent.ID = ""
	restarted.Grpc.Token = ""
	restarted.Grpc.RegistrationToken = "" // The enrollment secret is no longer needed.
	svc = NewAgentServiceImpl(&restarted, client, nil)
	t.Cleanup(func() { closeIdentity(t, svc) })
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if rpc.enrollments != 1 || len(rpc.requests) != 2 || rpc.requests[1].RegistrationToken != "" || restarted.Agent.ID != first.Agent.ID || restarted.Grpc.Token != first.Grpc.Token {
		t.Fatal("restart did not reuse exactly the durable identity")
	}
	data, err := os.ReadFile(filepath.Join(first.Agent.IdentityPersistence.Directory, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), first.Grpc.RegistrationToken) || strings.Contains(string(data), "storage") {
		t.Fatal("identity record includes unrelated secrets")
	}
}

func TestIdentityPersistenceDisabledDoesNotTouchStore(t *testing.T) {
	rpc := &restartRegistrationClient{}
	conf := persistenceConf(t)
	conf.Agent.IdentityPersistence.Enabled = false
	conf.Agent.ID = "synthetic-agent-1"
	conf.Grpc.Token = "synthetic-agent-1:synthetic-signature"
	svc := NewAgentServiceImpl(conf, &agentgrpc.ClientWrapper{AgentClient: rpc}, nil)
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(conf.Agent.IdentityPersistence.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled persistence accessed or created its directory")
	}
	closeIdentity(t, svc)
}

func TestIdentityPersistenceRequiresVerifiedTransportBeforeFiles(t *testing.T) {
	conf := persistenceConf(t)
	svc := NewAgentServiceImpl(conf, &agentgrpc.ClientWrapper{AgentClient: &restartRegistrationClient{}}, nil)
	if err := svc.PrepareIdentity(context.Background()); err == nil {
		t.Fatal("unverified transport accepted")
	}
	if _, err := os.Lstat(conf.Agent.IdentityPersistence.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unverified transport touched identity directory")
	}
}

func TestPersistentRegistrationMalformedOrLostResponseStaysPending(t *testing.T) {
	for _, name := range []string{"lost", "nil", "rejected", "missing-id", "missing-token", "mismatched-token"} {
		t.Run(name, func(t *testing.T) {
			conf := persistenceConf(t)
			rpc := &persistenceRPC{register: func(_ context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
				pending, err := os.ReadFile(filepath.Join(conf.Agent.IdentityPersistence.Directory, "enrollment.pending"))
				if err != nil {
					t.Fatal("RPC happened before durable pending marker", err)
				}
				if strings.Contains(string(pending), req.RegistrationToken) {
					t.Fatal("pending marker persisted enrollment secret")
				}
				switch name {
				case "lost":
					return nil, status.Error(codes.Unavailable, "synthetic error contains "+req.RegistrationToken)
				case "nil":
					return nil, nil
				case "rejected":
					return &agentv1.RegisterResponse{Message: req.RegistrationToken}, nil
				case "missing-id":
					return &agentv1.RegisterResponse{Success: true, AuthToken: "synthetic-agent-1:signature"}, nil
				case "missing-token":
					return &agentv1.RegisterResponse{Success: true, AgentId: "synthetic-agent-1"}, nil
				default:
					return &agentv1.RegisterResponse{Success: true, AgentId: "synthetic-agent-1", AuthToken: "other:signature"}, nil
				}
			}}
			client := syntheticTLSClient(t, rpc)
			svc := NewAgentServiceImpl(conf, client, nil)
			if _, err := svc.Register(context.Background(), nil); err == nil || strings.Contains(err.Error(), conf.Grpc.RegistrationToken) {
				t.Fatal("malformed response accepted or leaked token", err)
			}
			if conf.Agent.ID != "" || conf.Grpc.Token != "" {
				t.Fatal("failed enrollment mutated runtime identity")
			}
			if _, err := svc.Register(context.Background(), nil); err == nil || rpc.calls != 1 {
				t.Fatal("uncertain enrollment was retried")
			}
			closeIdentity(t, svc)
			// Even explicit credentials cannot override an ambiguous pending store.
			conf.Agent.ID = "synthetic-agent-1"
			conf.Grpc.Token = "synthetic-agent-1:signature"
			restarted := NewAgentServiceImpl(conf, client, nil)
			if _, err := restarted.Register(context.Background(), nil); !errors.Is(err, identitystore.ErrPending) {
				t.Fatalf("pending store bypassed: %v", err)
			}
			if rpc.calls != 1 {
				t.Fatal("pending restart contacted controller")
			}
		})
	}
}

func TestPersistentExplicitOverrideIsRunOnlyAndWrongTokenDoesNotEnroll(t *testing.T) {
	rpc := &restartRegistrationClient{}
	client := syntheticTLSClient(t, rpc)
	conf := persistenceConf(t)
	svc := NewAgentServiceImpl(conf, client, nil)
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	closeIdentity(t, svc)
	path := filepath.Join(conf.Agent.IdentityPersistence.Directory, "identity.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conf.Grpc.Token = "synthetic-agent-1:wrong-signature"
	svc = NewAgentServiceImpl(conf, client, nil)
	if _, err := svc.Register(context.Background(), nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong explicit token did not fail closed: %v", err)
	}
	closeIdentity(t, svc)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || rpc.enrollments != 1 || rpc.requests[1].Token != conf.Grpc.Token || rpc.requests[1].RegistrationToken != "" {
		t.Fatal("explicit token override persisted or fell back to enrollment")
	}
	conf.Agent.ID = "other-agent"
	conf.Grpc.Token = "other-agent:signature"
	svc = NewAgentServiceImpl(conf, client, nil)
	if _, err := svc.Register(context.Background(), nil); err == nil || len(rpc.requests) != 2 {
		t.Fatal("wrong identity bypassed stored binding")
	}
	closeIdentity(t, svc)
}

func TestEmptyStoreExplicitIdentityDoesNotImport(t *testing.T) {
	rpc := &restartRegistrationClient{}
	conf := persistenceConf(t)
	conf.Agent.ID = "synthetic-agent-1"
	conf.Grpc.Token = "synthetic-agent-1:synthetic-signature"
	svc := NewAgentServiceImpl(conf, syntheticTLSClient(t, rpc), nil)
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	closeIdentity(t, svc)
	for _, name := range []string{"identity.json", "enrollment.pending"} {
		if _, err := os.Lstat(filepath.Join(conf.Agent.IdentityPersistence.Directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("explicit identity was implicitly imported")
		}
	}
}

func TestIdentityCloseWaitsForRegistrationPersistence(t *testing.T) {
	conf := persistenceConf(t)
	entered, release, registered, closed := make(chan struct{}), make(chan struct{}), make(chan error, 1), make(chan error, 1)
	rpc := &persistenceRPC{register: func(context.Context, *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
		close(entered)
		<-release
		return &agentv1.RegisterResponse{Success: true, AgentId: "synthetic-agent", AuthToken: "synthetic-agent:signature"}, nil
	}}
	client := syntheticTLSClient(t, rpc)
	svc := NewAgentServiceImpl(conf, client, nil)
	safe.Go(func() { _, err := svc.Register(context.Background(), nil); registered <- err })
	<-entered
	safe.Go(func() { closed <- svc.CloseIdentity() })
	select {
	case err := <-closed:
		t.Fatal("store lock released before registration resolved", err)
	case <-time.After(20 * time.Millisecond):
	}
	binding, err := client.ControllerIdentityBinding()
	if err != nil {
		t.Fatal(err)
	}
	cfg := identitystore.Config{Directory: conf.Agent.IdentityPersistence.Directory, Workspace: conf.Agent.WorkspaceDir, ControllerBinding: binding}
	if store, err := identitystore.Open(context.Background(), cfg); !errors.Is(err, identitystore.ErrLocked) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("in-flight enrollment did not retain process lock", err)
	}
	close(release)
	if err := <-registered; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	store, err := identitystore.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if id, ok := store.Identity(); !ok || id.AgentID != "synthetic-agent" {
		t.Fatal("response was not durable before releasing process lock")
	}
	if _, err := svc.Register(context.Background(), nil); err == nil {
		t.Fatal("closed service accepted registration")
	}
}

func TestInitialHeartbeatFailureDoesNotSchedule(t *testing.T) {
	cron.Init(nil)
	_ = cron.Remove("agent-heartbeat")
	secret := "synthetic-agent:secret"
	rpc := &persistenceRPC{register: func(context.Context, *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
		return &agentv1.RegisterResponse{Success: true, AgentId: "synthetic-agent"}, nil
	}, heartbeat: func(context.Context, *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
		return nil, status.Error(codes.Unauthenticated, secret)
	}}
	conf := &config.AgentConfig{Agent: config.AgentInfo{ID: "synthetic-agent"}, Grpc: config.GrpcConfig{Token: secret}}
	svc := NewAgentServiceImpl(conf, &agentgrpc.ClientWrapper{AgentClient: rpc}, nil)
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Heartbeat(context.Background(), nil); status.Code(err) != codes.Unauthenticated || strings.Contains(err.Error(), secret) {
		t.Fatal("initial heartbeat did not fail safely", err)
	}
	if svc.Authenticated() {
		t.Fatal("failed heartbeat marked service authenticated")
	}
	for _, entry := range cron.Entries() {
		if entry.Name == "agent-heartbeat" {
			t.Fatal("periodic heartbeat scheduled before initial authentication")
		}
	}
	closeIdentity(t, svc)
}

func TestPartialExplicitIdentityNeverLoadsOrEnrolls(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, pair := range [][2]string{{"synthetic-agent", ""}, {"", "synthetic-agent:signature"}, {" ", " "}, {" ", "synthetic-agent:signature"}} {
			conf := persistenceConf(t)
			conf.Agent.IdentityPersistence.Enabled = enabled
			conf.Agent.ID, conf.Grpc.Token = pair[0], pair[1]
			rpc := &restartRegistrationClient{}
			svc := NewAgentServiceImpl(conf, &agentgrpc.ClientWrapper{AgentClient: rpc}, nil)
			if _, err := svc.Register(context.Background(), nil); err == nil {
				t.Fatal("partial explicit identity was accepted")
			}
			if len(rpc.requests) != 0 {
				t.Fatal("partial explicit identity contacted controller")
			}
			if _, err := os.Lstat(conf.Agent.IdentityPersistence.Directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial explicit identity accessed store")
			}
		}
	}
}

func TestPersistentRestoredRejectedTokenDoesNotEnroll(t *testing.T) {
	firstRPC := &restartRegistrationClient{}
	client := syntheticTLSClient(t, firstRPC)
	conf := persistenceConf(t)
	svc := NewAgentServiceImpl(conf, client, nil)
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	closeIdentity(t, svc)
	conf.Agent.ID = ""
	conf.Grpc.Token = ""
	rpc := &persistenceRPC{register: func(_ context.Context, req *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
		if req.RegistrationToken != "" || req.AgentId != "synthetic-agent-1" {
			t.Fatal("restored token was bypassed")
		}
		return nil, status.Error(codes.PermissionDenied, "synthetic revoked token")
	}}
	client.AgentClient = rpc
	svc = NewAgentServiceImpl(conf, client, nil)
	if _, err := svc.Register(context.Background(), nil); status.Code(err) != codes.PermissionDenied {
		t.Fatal("revoked token was not rejected", err)
	}
	if _, err := svc.Register(context.Background(), nil); err == nil || rpc.calls != 1 {
		t.Fatal("revoked credentials fell back or retried")
	}
	closeIdentity(t, svc)
}

func TestPendingApprovalPersistsWithoutAuthenticatedRuntime(t *testing.T) {
	conf := persistenceConf(t)
	rpc := &persistenceRPC{register: func(context.Context, *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
		return &agentv1.RegisterResponse{Success: true, PendingApproval: true, AgentId: "synthetic-pending-agent", AuthToken: "synthetic-pending-agent:signature", Storage: &agentv1.StorageConfig{Provider: "minio", Endpoint: "synthetic-storage.invalid:9000", AccessKey: "synthetic-access", SecretKey: "synthetic-storage-secret"}}, nil
	}}
	svc := NewAgentServiceImpl(conf, syntheticTLSClient(t, rpc), nil)
	if _, err := svc.Register(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if svc.Authenticated() {
		t.Fatal("pending approval agent marked authenticated")
	}
	if svc.StorageHolder().Get() != nil {
		t.Fatal("pending approval response initialized storage")
	}
	if _, err := svc.Heartbeat(context.Background(), nil); err == nil {
		t.Fatal("pending approval agent sent heartbeat")
	}
	closeIdentity(t, svc)
	if _, err := os.Stat(filepath.Join(conf.Agent.IdentityPersistence.Directory, "identity.json")); err != nil {
		t.Fatal("pending approval identity was not persisted", err)
	}
}

func TestEnrollmentRechecksWorkspaceAfterConnectionWait(t *testing.T) {
	conf := persistenceConf(t)
	rpc := &restartRegistrationClient{}
	svc := NewAgentServiceImpl(conf, syntheticTLSClient(t, rpc), nil)
	defer closeIdentity(t, svc)
	if err := svc.PrepareIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(conf.Agent.WorkspaceDir, ".terminal-report-outbox"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(context.Background(), nil); err == nil || len(rpc.requests) != 0 {
		t.Fatal("enrollment used stale workspace safety check")
	}
	if _, err := os.Lstat(filepath.Join(conf.Agent.IdentityPersistence.Directory, "enrollment.pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe workspace began enrollment")
	}
}
