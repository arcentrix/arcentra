package config

import (
	grpcclient "github.com/arcentrix/arcentra/internal/shared/grpc"
	"testing"
)

func TestAgentGRPCAddressAndExplicitTLS(t *testing.T) {
	for _, tc := range []struct {
		input string
		tls   bool
		want  string
	}{
		{"controller.test:9090", false, "controller.test:9090"},
		{"https://controller.test:9443", true, "controller.test:9443"},
		{"controller.test", true, "controller.test:9090"},
		{"[::1]:9090", true, "[::1]:9090"},
		{"http://127.0.0.1:9090", false, "127.0.0.1:9090"},
		{"https://controller.test", false, ""},
		{"http://controller.test", true, ""},
		{"https://user:secret@controller.test", true, ""},
		{"https://controller.test/path", true, ""},
		{"https://controller.test?token=secret", true, ""},
		{"controller.test:invalid", true, ""},
		{" controller.test:9090", true, ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			conf := AgentConfig{Grpc: GrpcConfig{ServerAddr: tc.input, TLS: grpcclient.ClientTLSConfig{Enabled: tc.tls, ServerName: "controller.test"}}}
			err := conf.parseServerAddr()
			if (err == nil) != (tc.want != "") {
				t.Fatalf("parse success %v want %q", err, tc.want)
			}
			if err == nil && conf.Grpc.ServerAddr != tc.want {
				t.Fatalf("got %s want %s", conf.Grpc.ServerAddr, tc.want)
			}
			if provided := ProvideGrpcClientConfig(&conf); provided.TLS != conf.Grpc.TLS {
				t.Fatal("TLS policy lost in dependency injection")
			}
		})
	}
}
