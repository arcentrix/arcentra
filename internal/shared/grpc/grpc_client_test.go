package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestUpdatedTokenPreservesOutgoingMetadata(t *testing.T) {
	wrapper := &ClientWrapper{config: ClientConf{Token: "original"}}
	wrapper.UpdateToken("registered")
	original := metadata.Pairs("traceparent", "trace-value", "authorization", "old")
	ctx := wrapper.WithAuthContext(metadata.NewOutgoingContext(context.Background(), original))
	md, _ := metadata.FromOutgoingContext(ctx)
	if auth := md.Get("authorization"); len(auth) != 1 || auth[0] != "bearer registered" {
		t.Fatalf("wrong token: %v", auth)
	}
	if md.Get("traceparent")[0] != "trace-value" {
		t.Fatal("trace metadata discarded")
	}
	if original.Get("authorization")[0] != "old" {
		t.Fatal("caller metadata mutated")
	}
	if err := wrapper.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wrapper.Close(); err != nil {
		t.Fatal(err)
	}
}
