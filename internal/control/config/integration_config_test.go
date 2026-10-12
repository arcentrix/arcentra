package config

import (
	"strings"
	"testing"

	"github.com/arcentrix/arcentra/internal/control/copilot"
	"github.com/arcentrix/arcentra/internal/shared/deployment"
	"github.com/bytedance/sonic"
)

func TestIntegrationConfigurationNeverAppearsInJSON(t *testing.T) {
	cfg := AppConfig{
		Copilot:    copilot.Config{Enabled: true, AllowedProjectIDs: []string{"private-project"}, Providers: []copilot.ProviderConfig{{ID: "private-provider", Kind: "anthropic", BaseURL: "https://private-model.example", APIKeyEnv: "PRIVATE_MODEL_KEY", Models: []string{"private-model"}}}},
		Deployment: deployment.Config{Enabled: true, Targets: []deployment.TargetConfig{{Ref: "private-cluster", ProjectID: "private-project", Server: "https://private-cluster.example", BearerTokenFile: "/private/cluster-token", CAFile: "/private/cluster-ca", Namespace: "private-namespace", Deployment: "private-workload", Container: "private-container"}}},
	}
	data, err := sonic.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"private-", "PRIVATE_MODEL_KEY", "/private/", "copilot", "deployment"} {
		if strings.Contains(string(data), value) {
			t.Fatalf("private integration configuration serialized: %s", value)
		}
	}
}
