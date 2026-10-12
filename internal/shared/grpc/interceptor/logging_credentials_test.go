package interceptor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/pkg/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCredentialBearingRPCErrorIsNotLogged(t *testing.T) {
	directory := t.TempDir()
	conf := log.SetDefaults()
	conf.Output = "file"
	conf.Path = directory
	conf.Filename = "redacted.log"
	if err := log.Init(conf); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Init(log.SetDefaults()) })
	const secret = "synthetic-private-enrollment-token-canary"
	failed := status.Error(codes.Unauthenticated, "rejected "+secret)
	method := "/agent.v1.AgentService/Register"
	for i := 0; i < 3; i++ {
		_, err := LoggingUnaryInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, func(context.Context, any) (any, error) { return nil, failed })
		if err != failed {
			t.Fatal("server caller error changed")
		}
		err = LoggingUnaryClientInterceptor()(context.Background(), method, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return failed })
		if err != failed {
			t.Fatal("client caller error changed")
		}
		logCall("stream", method, 0, failed)
		logClientCall("stream", method, 0, failed)
	}
	_ = log.Sync()
	data, err := os.ReadFile(filepath.Join(directory, "redacted.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("credential leaked into RPC logs")
	}
	if !strings.Contains(string(data), "Unauthenticated") || !strings.Contains(string(data), method) {
		t.Fatal("safe diagnostics missing")
	}
}
