package tracectx

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type credentialServerStream struct{ grpc.ServerStream }

func (credentialServerStream) Context() context.Context { return context.Background() }

type credentialClientStream struct {
	grpc.ClientStream
	failure error
}

func (s credentialClientStream) CloseSend() error { return s.failure }

func TestGRPCTracingDoesNotRecordCredentialBearingErrors(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = provider.Shutdown(context.Background()) })
	const secret = "synthetic-sensitive-enrollment-canary"
	failed := status.Error(codes.Unauthenticated, "rejected "+secret)
	method := "/agent.v1.AgentService/Register"
	for attempt := 0; attempt < 2; attempt++ {
		_, err := UnaryServerInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method}, func(context.Context, any) (any, error) { return nil, failed })
		if err != failed {
			t.Fatal("server error changed")
		}
		err = UnaryClientInterceptor()(context.Background(), method, nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return failed })
		if err != failed {
			t.Fatal("client error changed")
		}
		err = StreamServerInterceptor()(nil, credentialServerStream{}, &grpc.StreamServerInfo{FullMethod: method}, func(any, grpc.ServerStream) error { return failed })
		if err != failed {
			t.Fatal("stream server error changed")
		}
		_, err = StreamClientInterceptor()(context.Background(), &grpc.StreamDesc{}, nil, method, func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
			return nil, failed
		})
		if err != failed {
			t.Fatal("stream client error changed")
		}
		_, span := provider.Tracer("test").Start(context.Background(), "close-send")
		stream := &wrappedClientStream{ClientStream: credentialClientStream{failure: failed}, span: span}
		if stream.CloseSend() != failed {
			t.Fatal("close send error changed")
		}
	}
	spans := recorder.Ended()
	if len(spans) != 10 {
		t.Fatalf("got %d spans want10", len(spans))
	}
	for _, span := range spans {
		if strings.Contains(fmt.Sprint(span.Status(), span.Attributes(), span.Events()), secret) {
			t.Fatal("credential leaked into trace")
		}
		if span.Status().Description != "Unauthenticated" {
			t.Fatalf("unsafe/unhelpful trace status %q", span.Status().Description)
		}
	}
}
