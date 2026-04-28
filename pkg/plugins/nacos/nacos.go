// Copyright 2025 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package nacos

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
	"github.com/go-resty/resty/v2"
)

const nacosDefaultGroup = "DEFAULT_GROUP"

// Config is the plugin-level configuration.
type Config struct {
	ServerAddr string `json:"serverAddr"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Namespace  string `json:"namespace"`
	Timeout    int    `json:"timeout"`
}

// authToken caches the Nacos access-token and its expiry.
type authToken struct {
	mu          sync.Mutex
	accessToken string
	expireAt    time.Time
}

// ConfigGetArgs holds parameters for the config.get action.
type ConfigGetArgs struct {
	ServerAddr string `json:"server_addr"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Namespace  string `json:"namespace"`
	DataID     string `json:"data_id"`
	Group      string `json:"group"`
}

// ConfigPublishArgs holds parameters for the config.publish action.
type ConfigPublishArgs struct {
	ServerAddr  string `json:"server_addr"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Namespace   string `json:"namespace"`
	DataID      string `json:"data_id"`
	Group       string `json:"group"`
	Content     string `json:"content"`
	ContentFile string `json:"content_file"`
	Type        string `json:"type"`
}

// ConfigDeleteArgs holds parameters for the config.delete action.
type ConfigDeleteArgs struct {
	ServerAddr string `json:"server_addr"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Namespace  string `json:"namespace"`
	DataID     string `json:"data_id"`
	Group      string `json:"group"`
}

// Nacos implements plugin.Plugin for Nacos configuration centre.
type Nacos struct {
	*plugin.Base
	cfg   Config
	token authToken
}

func NewNacos() *Nacos {
	p := &Nacos{
		Base: plugin.NewPluginBase(),
		cfg: Config{
			Timeout: 30,
		},
	}
	p.registerActions()
	return p
}

func (p *Nacos) registerActions() {
	_ = p.Registry().RegisterFunc("config.get", "Get a configuration from Nacos",
		func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
			return p.configGet(params, opts)
		})
	_ = p.Registry().RegisterFunc("config.publish", "Publish (create/update) a configuration to Nacos",
		func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
			return p.configPublish(params, opts)
		})
	_ = p.Registry().RegisterFunc("config.delete", "Delete a configuration from Nacos",
		func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
			return p.configDelete(params, opts)
		})
}

func (p *Nacos) Name() string        { return "nacos" }
func (p *Nacos) Description() string { return "Nacos configuration centre plugin" }
func (p *Nacos) Version() string     { return "1.0.0" }
func (p *Nacos) Type() plugin.Type   { return plugin.TypeIntegration }
func (p *Nacos) Author() string      { return "Arcentra Authors." }
func (p *Nacos) Repository() string  { return "https://github.com/arcentrix/arcentra" }

func (p *Nacos) Init(config json.RawMessage) error {
	if len(config) > 0 {
		if err := sonic.Unmarshal(config, &p.cfg); err != nil {
			return fmt.Errorf("failed to parse nacos config: %w", err)
		}
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 30
	}
	log.Infow("nacos plugin initialized", "plugin", "nacos", "server_addr", p.cfg.ServerAddr)
	return nil
}

func (p *Nacos) Cleanup() error {
	log.Infow("nacos plugin cleanup completed", "plugin", "nacos")
	return nil
}

func (p *Nacos) Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	return p.Base.Execute(action, params, opts)
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func (p *Nacos) configGet(params json.RawMessage, _ json.RawMessage) (json.RawMessage, error) {
	var args ConfigGetArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse config.get params: %w", err)
	}
	p.applyDefaults(&args.ServerAddr, &args.Username, &args.Password, &args.Namespace)
	if args.ServerAddr == "" {
		return nil, fmt.Errorf("server_addr is required")
	}
	if args.DataID == "" {
		return nil, fmt.Errorf("data_id is required")
	}
	if args.Group == "" {
		args.Group = nacosDefaultGroup
	}

	client := p.newClient(args.ServerAddr)
	token, err := p.ensureToken(client, args.ServerAddr, args.Username, args.Password)
	if err != nil {
		return nil, err
	}

	query := map[string]string{
		"dataId": args.DataID,
		"group":  args.Group,
	}
	if args.Namespace != "" {
		query["tenant"] = args.Namespace
	}
	if token != "" {
		query["accessToken"] = token
	}

	resp, err := client.R().SetQueryParams(query).Get(args.ServerAddr + "/nacos/v1/cs/configs")
	if err != nil {
		return nil, fmt.Errorf("nacos request failed: %w", err)
	}
	if resp.StatusCode() == 404 {
		return nil, fmt.Errorf("configuration not found: dataId=%s, group=%s", args.DataID, args.Group)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("nacos returned %d: %s", resp.StatusCode(), resp.String())
	}

	return sonic.Marshal(map[string]any{
		"success": true,
		"content": resp.String(),
	})
}

func (p *Nacos) configPublish(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var args ConfigPublishArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse config.publish params: %w", err)
	}
	p.applyDefaults(&args.ServerAddr, &args.Username, &args.Password, &args.Namespace)
	if args.ServerAddr == "" {
		return nil, fmt.Errorf("server_addr is required")
	}
	if args.DataID == "" {
		return nil, fmt.Errorf("data_id is required")
	}
	if args.Group == "" {
		args.Group = nacosDefaultGroup
	}

	content, err := p.resolveContent(args.Content, args.ContentFile, opts)
	if err != nil {
		return nil, err
	}
	if content == "" {
		return nil, fmt.Errorf("either content or content_file is required")
	}

	client := p.newClient(args.ServerAddr)
	token, err := p.ensureToken(client, args.ServerAddr, args.Username, args.Password)
	if err != nil {
		return nil, err
	}

	formData := map[string]string{
		"dataId":  args.DataID,
		"group":   args.Group,
		"content": content,
	}
	if args.Namespace != "" {
		formData["tenant"] = args.Namespace
	}
	if args.Type != "" {
		formData["type"] = args.Type
	}
	if token != "" {
		formData["accessToken"] = token
	}

	resp, err := client.R().SetFormData(formData).Post(args.ServerAddr + "/nacos/v1/cs/configs")
	if err != nil {
		return nil, fmt.Errorf("nacos request failed: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("nacos returned %d: %s", resp.StatusCode(), redact(resp.String(), args.Password))
	}

	return sonic.Marshal(map[string]any{
		"success": true,
		"data":    resp.String(),
	})
}

func (p *Nacos) configDelete(params json.RawMessage, _ json.RawMessage) (json.RawMessage, error) {
	var args ConfigDeleteArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse config.delete params: %w", err)
	}
	p.applyDefaults(&args.ServerAddr, &args.Username, &args.Password, &args.Namespace)
	if args.ServerAddr == "" {
		return nil, fmt.Errorf("server_addr is required")
	}
	if args.DataID == "" {
		return nil, fmt.Errorf("data_id is required")
	}
	if args.Group == "" {
		args.Group = nacosDefaultGroup
	}

	client := p.newClient(args.ServerAddr)
	token, err := p.ensureToken(client, args.ServerAddr, args.Username, args.Password)
	if err != nil {
		return nil, err
	}

	query := map[string]string{
		"dataId": args.DataID,
		"group":  args.Group,
	}
	if args.Namespace != "" {
		query["tenant"] = args.Namespace
	}
	if token != "" {
		query["accessToken"] = token
	}

	resp, err := client.R().SetQueryParams(query).Delete(args.ServerAddr + "/nacos/v1/cs/configs")
	if err != nil {
		return nil, fmt.Errorf("nacos request failed: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("nacos returned %d: %s", resp.StatusCode(), resp.String())
	}

	return sonic.Marshal(map[string]any{
		"success": true,
		"data":    resp.String(),
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (p *Nacos) applyDefaults(serverAddr, username, password, namespace *string) {
	if *serverAddr == "" {
		*serverAddr = p.cfg.ServerAddr
	}
	if *username == "" {
		*username = p.cfg.Username
	}
	if *password == "" {
		*password = p.cfg.Password
	}
	if *namespace == "" {
		*namespace = p.cfg.Namespace
	}
}

func (p *Nacos) newClient(serverAddr string) *resty.Client {
	_ = serverAddr
	return resty.New().SetTimeout(time.Duration(p.cfg.Timeout) * time.Second)
}

// ensureToken obtains (and caches) a Nacos access-token when credentials are
// provided. Returns "" when authentication is not required.
func (p *Nacos) ensureToken(client *resty.Client, serverAddr, username, password string) (string, error) {
	if username == "" || password == "" {
		return "", nil
	}

	p.token.mu.Lock()
	defer p.token.mu.Unlock()

	if p.token.accessToken != "" && time.Now().Before(p.token.expireAt) {
		return p.token.accessToken, nil
	}

	resp, err := client.R().
		SetFormData(map[string]string{"username": username, "password": password}).
		Post(serverAddr + "/nacos/v1/auth/login")
	if err != nil {
		return "", fmt.Errorf("nacos auth request failed: %w", err)
	}
	if resp.IsError() {
		return "", fmt.Errorf("nacos auth failed (%d): %s", resp.StatusCode(), redact(resp.String(), password))
	}

	var result struct {
		AccessToken string `json:"accessToken"`
		TokenTTL    int64  `json:"tokenTtl"`
	}
	if err := sonic.Unmarshal(resp.Body(), &result); err != nil {
		return "", fmt.Errorf("failed to parse nacos auth response: %w", err)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("nacos auth returned empty token")
	}

	p.token.accessToken = result.AccessToken
	if result.TokenTTL > 0 {
		p.token.expireAt = time.Now().Add(time.Duration(result.TokenTTL) * time.Second / 2)
	} else {
		p.token.expireAt = time.Now().Add(15 * time.Minute)
	}
	return p.token.accessToken, nil
}

// resolveContent returns the configuration content, preferring content_file
// over inline content. The workspace path is extracted from opts.
func (p *Nacos) resolveContent(content, contentFile string, opts json.RawMessage) (string, error) {
	if contentFile == "" {
		return content, nil
	}
	workspace := parseWorkspace(opts)
	data, err := readWorkspaceFile(workspace, contentFile)
	if err != nil {
		return "", fmt.Errorf("read content_file: %w", err)
	}
	return string(data), nil
}

// ---------------------------------------------------------------------------
// Shared file-reading utilities
// ---------------------------------------------------------------------------

func parseWorkspace(opts json.RawMessage) string {
	if len(opts) == 0 {
		return ""
	}
	var m map[string]any
	if err := sonic.Unmarshal(opts, &m); err != nil {
		return ""
	}
	if ws, ok := m["workspace"].(string); ok {
		return ws
	}
	return ""
}

// readWorkspaceFile reads a file relative to the workspace directory with
// path-traversal protection.
func readWorkspaceFile(workspace, relPath string) ([]byte, error) {
	if relPath == "" {
		return nil, fmt.Errorf("file path is empty")
	}
	if workspace == "" {
		return nil, fmt.Errorf("workspace is not available, cannot resolve file path %q", relPath)
	}
	absPath := filepath.Join(workspace, relPath)
	absPath = filepath.Clean(absPath)
	wsClean := filepath.Clean(workspace)
	if !strings.HasPrefix(absPath, wsClean+string(filepath.Separator)) && absPath != wsClean {
		return nil, fmt.Errorf("file path %q escapes workspace boundary", relPath)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %q: %w", relPath, err)
	}
	return data, nil
}

func redact(s, secret string) string {
	if secret == "" || s == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

func init() {
	plugin.Register(NewNacos())
}
