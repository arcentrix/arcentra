package scm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	scmpkg "github.com/arcentrix/arcentra/pkg/scm"
	_ "github.com/arcentrix/arcentra/pkg/scm/builtin"
	"github.com/bytedance/sonic"
)

type Plugin struct {
	*plugin.PluginBase
	cfg scmpkg.ProviderConfig
}

type parseWebhookArgs struct {
	Provider   scmpkg.ProviderConfig `json:"provider"`
	Secret     string                `json:"secret"`
	Headers    map[string]string     `json:"headers"`
	Body       string                `json:"body,omitempty"`
	BodyBase64 string                `json:"bodyBase64,omitempty"`
}

type pollEventsArgs struct {
	Provider scmpkg.ProviderConfig `json:"provider"`
	Repo     scmpkg.Repo           `json:"repo"`
	Cursor   scmpkg.Cursor         `json:"cursor"`
}

func New() *Plugin {
	p := &Plugin{
		PluginBase: plugin.NewPluginBase(),
		cfg: scmpkg.ProviderConfig{
			Kind: scmpkg.ProviderKindGitHub,
		},
	}
	p.registerActions()
	return p
}

func (p *Plugin) Name() string        { return "scm" }
func (p *Plugin) Description() string { return "SCM integration plugin for webhook and polling" }
func (p *Plugin) Version() string     { return "1.0.0" }
func (p *Plugin) Type() plugin.PluginType {
	return plugin.TypeIntegration
}
func (p *Plugin) Author() string     { return "Arcentra Team" }
func (p *Plugin) Repository() string { return "https://github.com/arcentrix/arcentra" }

func (p *Plugin) Init(config json.RawMessage) error {
	if len(config) > 0 {
		if err := sonic.Unmarshal(config, &p.cfg); err != nil {
			return fmt.Errorf("failed to parse config: %w", err)
		}
	}
	log.Infow("scm plugin initialized", "plugin", "scm", "kind", p.cfg.Kind)
	return nil
}

func (p *Plugin) Cleanup() error { return nil }

func (p *Plugin) Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	return p.PluginBase.Execute(action, params, opts)
}

func (p *Plugin) registerActions() {
	_ = p.Registry().RegisterFunc("webhook.parse", "Verify and parse webhook payload into normalized events", p.webhookParse)
	_ = p.Registry().RegisterFunc("events.poll", "Poll SCM events from provider API", p.eventsPoll)
}

func (p *Plugin) webhookParse(params json.RawMessage, _ json.RawMessage) (json.RawMessage, error) {
	var args parseWebhookArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse webhook params: %w", err)
	}
	cfg := args.Provider
	if cfg.Kind == "" {
		cfg = p.cfg
	}
	prov, err := scmpkg.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	body := []byte(args.Body)
	if args.BodyBase64 != "" {
		raw, err := base64.StdEncoding.DecodeString(args.BodyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode bodyBase64: %w", err)
		}
		body = raw
	}

	req := scmpkg.WebhookRequest{
		Headers: args.Headers,
		Body:    body,
	}
	if err := prov.VerifyWebhook(context.Background(), req, args.Secret); err != nil {
		return nil, err
	}
	events, err := prov.ParseWebhook(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(map[string]any{
		"events": events,
	})
}

func (p *Plugin) eventsPoll(params json.RawMessage, _ json.RawMessage) (json.RawMessage, error) {
	var args pollEventsArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse poll params: %w", err)
	}
	cfg := args.Provider
	if cfg.Kind == "" {
		cfg = p.cfg
	}
	prov, err := scmpkg.NewProvider(cfg)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	events, next, err := prov.PollEvents(ctx, args.Repo, args.Cursor)
	if err != nil {
		return nil, err
	}
	return sonic.Marshal(map[string]any{
		"events":     events,
		"nextCursor": next,
	})
}

func init() {
	plugin.Register(New())
}
