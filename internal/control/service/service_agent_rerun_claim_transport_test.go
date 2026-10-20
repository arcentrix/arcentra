// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/arcentrix/arcentra/api/agent/v1"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/internal/control/service"
	"github.com/arcentrix/arcentra/internal/shared/grpc/interceptor"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/taskqueue"
	grpcrecovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type rerunWireStore struct {
	repo.IJobRunRepository
	calls     atomic.Int32
	panicMode atomic.Bool
}

func (s *rerunWireStore) ClaimAndFetchCopilotRerun(_ context.Context, in taskqueue.RerunClaimRequest) (*taskqueue.RerunClaimResult, error) {
	s.calls.Add(1)
	if s.panicMode.Load() {
		panic("private-payload-panic" + string(in.AdmissionNonce))
	}
	return nil, errors.New("private-payload-canary" + string(in.AdmissionNonce))
}

type rerunWireVerifier struct{ enabled bool }

func (v rerunWireVerifier) VerifyAgentToken(_ context.Context, token, _ string) (string, error) {
	if !v.enabled || token != "rerun-test-token" {
		return "", errors.New("fixture denied")
	}
	return "agent", nil
}
func rerunWireRequest() *agentv1.ClaimAndFetchCopilotRerunRequest {
	return &agentv1.ClaimAndFetchCopilotRerunRequest{Reference: &agentv1.CopilotRerunClaimReference{AgentId: "agent", PipelineId: "pipeline", RunId: "11111111-1111-4111-8111-111111111111", JobRunId: "22222222-2222-4222-8222-222222222222", GateId: "33333333-3333-4333-8333-333333333333", ScheduleId: "44444444-4444-4444-8444-444444444444", SealId: "55555555-5555-4555-8555-555555555555", SealDigest: "v1:" + strings.Repeat("a", 64), PayloadDigest: "v1:" + strings.Repeat("b", 64)}, ClaimId: "66666666-6666-4666-8666-666666666666", AdmissionNonce: bytes.Repeat([]byte("N"), 32)}
}

// Uses an actual TLS handshake and production auth/logging interceptors. Only
// token verification and absent future transaction implementation are test seams.
func TestRerunClaimTransportActualTLSAndInterceptor(t *testing.T) {
	store := &rerunWireStore{}
	request := rerunWireRequest()
	svc := service.NewAgentRerunClaimTestService(store)
	interceptor.SetTokenVerifier(rerunWireVerifier{enabled: true})
	t.Cleanup(func() { interceptor.SetTokenVerifier(rerunWireVerifier{}) })
	conf := log.SetDefaults()
	conf.Output = "file"
	conf.Path = t.TempDir()
	conf.Filename = "rerun.log"
	require.NoError(t, log.Init(conf))
	t.Cleanup(func() { _ = log.Init(log.SetDefaults()) })
	seed := httptest.NewTLSServer(http.NotFoundHandler())
	cert := seed.TLS.Certificates[0]
	root := seed.Certificate()
	seed.Close()
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		listener := bufconn.Listen(1024 * 1024)
		server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: version, MaxVersion: version, Certificates: []tls.Certificate{cert}})), grpc.ChainUnaryInterceptor(interceptor.LoggingUnaryInterceptor(), interceptor.AuthUnaryInterceptor(), grpcrecovery.UnaryServerInterceptor()))
		agentv1.RegisterAgentServiceServer(server, svc)
		go func() { _ = server.Serve(listener) }()
		roots := x509.NewCertPool()
		roots.AddCert(root)
		conn, err := grpc.NewClient("passthrough:///rerun", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: version, MaxVersion: version, RootCAs: roots, ServerName: "example.com"})), grpc.WithUnaryInterceptor(interceptor.LoggingUnaryClientInterceptor()))
		require.NoError(t, err)
		client := agentv1.NewAgentServiceClient(conn)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		calls := store.calls.Load()
		_, err = client.ClaimAndFetchCopilotRerun(ctx, request)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		require.Equal(t, calls, store.calls.Load())
		_, err = client.ClaimAndFetchCopilotRerun(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer wrong"), request)
		require.Equal(t, codes.Unauthenticated, status.Code(err))
		require.Equal(t, calls, store.calls.Load())
		good := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer rerun-test-token")
		foreign := proto.Clone(request).(*agentv1.ClaimAndFetchCopilotRerunRequest)
		foreign.Reference.AgentId = "other"
		_, err = client.ClaimAndFetchCopilotRerun(good, foreign)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Equal(t, calls, store.calls.Load())
		_, err = client.ClaimAndFetchCopilotRerun(good, request)
		require.Equal(t, codes.Unavailable, status.Code(err))
		require.Equal(t, calls+1, store.calls.Load())
		require.NotContains(t, err.Error(), "private-payload-canary")
		require.NotContains(t, err.Error(), string(request.AdmissionNonce))
		store.panicMode.Store(true)
		_, err = client.ClaimAndFetchCopilotRerun(good, request)
		require.Equal(t, codes.Unavailable, status.Code(err))
		require.NotContains(t, err.Error(), "private-payload-panic")
		require.NotContains(t, err.Error(), string(request.AdmissionNonce))
		store.panicMode.Store(false)
		_, err = client.UpdateLabels(good, &agentv1.UpdateLabelsRequest{})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		cancel()
		_ = conn.Close()
		server.Stop()
		_ = listener.Close()
	}
	calls := store.calls.Load()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer rerun-test-token"))
	info := &grpc.UnaryServerInfo{FullMethod: agentv1.AgentService_ClaimAndFetchCopilotRerun_FullMethodName}
	_, err := interceptor.AuthUnaryInterceptor()(ctx, request, info, func(c context.Context, r any) (any, error) {
		return svc.ClaimAndFetchCopilotRerun(c, r.(*agentv1.ClaimAndFetchCopilotRerunRequest))
	})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Equal(t, calls, store.calls.Load())
	_ = log.Sync()
	data, err := os.ReadFile(filepath.Join(conf.Path, conf.Filename))
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-payload-canary")
	require.NotContains(t, string(data), "private-payload-panic")
	require.NotContains(t, string(data), string(request.AdmissionNonce))
	require.Contains(t, string(data), info.FullMethod)
}
