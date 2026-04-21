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

package apollo

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
	"github.com/go-resty/resty/v2"
	"gopkg.in/yaml.v3"
)

// Config is the plugin-level configuration.
type Config struct {
	PortalURL string `json:"portalUrl"`
	Token     string `json:"token"`
	Timeout   int    `json:"timeout"`
}

// ---------------------------------------------------------------------------
// Action argument types
// ---------------------------------------------------------------------------

type ConfigGetArgs struct {
	PortalURL string `json:"portal_url"`
	Token     string `json:"token"`
	AppID     string `json:"app_id"`
	Env       string `json:"env"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

type ConfigUpdateArgs struct {
	PortalURL string `json:"portal_url"`
	Token     string `json:"token"`
	AppID     string `json:"app_id"`
	Env       string `json:"env"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     string `json:"value"`
	ValueFile string `json:"value_file"`
	Comment   string `json:"comment"`
	Operator  string `json:"operator"`
}

type ConfigDeleteArgs struct {
	PortalURL string `json:"portal_url"`
	Token     string `json:"token"`
	AppID     string `json:"app_id"`
	Env       string `json:"env"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Operator  string `json:"operator"`
}

type ConfigReleaseArgs struct {
	PortalURL    string `json:"portal_url"`
	Token        string `json:"token"`
	AppID        string `json:"app_id"`
	Env          string `json:"env"`
	Cluster      string `json:"cluster"`
	Namespace    string `json:"namespace"`
	ReleaseTitle string `json:"release_title"`
	ReleasedBy   string `json:"released_by"`
	Comment      string `json:"comment"`
}

type NamespaceImportArgs struct {
	PortalURL string `json:"portal_url"`
	Token     string `json:"token"`
	AppID     string `json:"app_id"`
	Env       string `json:"env"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	File      string `json:"file"`
	Format    string `json:"format"`
	Operator  string `json:"operator"`
}

// Apollo implements plugin.Plugin for the Apollo configuration centre.
type Apollo struct {
	*plugin.Base
	cfg Config
}

func NewApollo() *Apollo {
	p := &Apollo{
		Base: plugin.NewPluginBase(),
		cfg:  Config{Timeout: 30},
	}
	p.registerActions()
	return p
}

func (p *Apollo) registerActions() {
	_ = p.Registry().RegisterFunc("config.get", "Get a configuration item from Apollo",
		func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
			return p.configGet(params, opts)
		})
	_ = p.Registry().RegisterFunc("config.update", "Create or update a configuration item in Apollo",
		func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
			return p.configUpdate(params, opts)
		})
	_ = p.Registry().RegisterFunc("config.delete", "Delete a configuration item from Apollo",
		func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
			return p.configDelete(params, opts)
		})
	_ = p.Registry().RegisterFunc("config.release", "Release namespace configuration in Apollo",
		func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
			return p.configRelease(params, opts)
		})
	_ = p.Registry().RegisterFunc("namespace.import", "Batch-import configuration from a file into an Apollo namespace",
		func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
			return p.namespaceImport(params, opts)
		})
}

func (p *Apollo) Name() string        { return "apollo" }
func (p *Apollo) Description() string { return "Apollo configuration centre plugin" }
func (p *Apollo) Version() string     { return "1.0.0" }
func (p *Apollo) Type() plugin.Type   { return plugin.TypeIntegration }
func (p *Apollo) Author() string      { return "Arcentra Authors." }
func (p *Apollo) Repository() string  { return "https://github.com/arcentrix/arcentra" }

func (p *Apollo) Init(config json.RawMessage) error {
	if len(config) > 0 {
		if err := sonic.Unmarshal(config, &p.cfg); err != nil {
			return fmt.Errorf("failed to parse apollo config: %w", err)
		}
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 30
	}
	log.Infow("apollo plugin initialized", "plugin", "apollo", "portal_url", p.cfg.PortalURL)
	return nil
}

func (p *Apollo) Cleanup() error {
	log.Infow("apollo plugin cleanup completed", "plugin", "apollo")
	return nil
}

func (p *Apollo) Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	return p.Base.Execute(action, params, opts)
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func (p *Apollo) configGet(params json.RawMessage, _ json.RawMessage) (json.RawMessage, error) {
	var args ConfigGetArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse config.get params: %w", err)
	}
	p.applyConnDefaults(&args.PortalURL, &args.Token)
	if err := requireFields("config.get", map[string]string{
		"portal_url": args.PortalURL,
		"app_id":     args.AppID,
		"env":        args.Env,
		"key":        args.Key,
	}); err != nil {
		return nil, err
	}
	fillClusterNamespace(&args.Cluster, &args.Namespace)

	url := p.itemURL(args.PortalURL, args.Env, args.AppID, args.Cluster, args.Namespace, args.Key)
	resp, err := p.newClient().R().
		SetHeader("Authorization", args.Token).
		Get(url)
	if err != nil {
		return nil, fmt.Errorf("apollo request failed: %w", err)
	}
	if resp.StatusCode() == http.StatusNotFound {
		return nil, fmt.Errorf("configuration not found: key=%s in %s/%s", args.Key, args.AppID, args.Namespace)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("apollo returned %d: %s", resp.StatusCode(), resp.String())
	}

	var item map[string]any
	if err := sonic.Unmarshal(resp.Body(), &item); err != nil {
		return nil, fmt.Errorf("failed to parse apollo response: %w", err)
	}
	return sonic.Marshal(map[string]any{"success": true, "data": item})
}

func (p *Apollo) configUpdate(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var args ConfigUpdateArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse config.update params: %w", err)
	}
	p.applyConnDefaults(&args.PortalURL, &args.Token)
	if err := requireFields("config.update", map[string]string{
		"portal_url": args.PortalURL,
		"app_id":     args.AppID,
		"env":        args.Env,
		"key":        args.Key,
		"operator":   args.Operator,
	}); err != nil {
		return nil, err
	}
	fillClusterNamespace(&args.Cluster, &args.Namespace)

	value, err := resolveValue(args.Value, args.ValueFile, opts)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, fmt.Errorf("either value or value_file is required for config.update")
	}

	url := p.itemURL(args.PortalURL, args.Env, args.AppID, args.Cluster, args.Namespace, args.Key)

	// Try to get the item first to decide create vs update.
	client := p.newClient()
	getResp, err := client.R().
		SetHeader("Authorization", args.Token).
		Get(url)
	if err != nil {
		return nil, fmt.Errorf("apollo request failed: %w", err)
	}

	var body map[string]any
	if getResp.StatusCode() == http.StatusNotFound {
		body = map[string]any{
			"key":                 args.Key,
			"value":               value,
			"comment":             args.Comment,
			"dataChangeCreatedBy": args.Operator,
		}
		resp, err := client.R().
			SetHeader("Authorization", args.Token).
			SetHeader("Content-Type", "application/json;charset=UTF-8").
			SetBody(body).
			Post(p.itemsURL(args.PortalURL, args.Env, args.AppID, args.Cluster, args.Namespace))
		if err != nil {
			return nil, fmt.Errorf("apollo create item failed: %w", err)
		}
		if resp.IsError() {
			return nil, fmt.Errorf("apollo create item returned %d: %s", resp.StatusCode(), redact(resp.String(), args.Token))
		}
		return sonic.Marshal(map[string]any{"success": true, "action": "created"})
	}

	body = map[string]any{
		"key":                      args.Key,
		"value":                    value,
		"comment":                  args.Comment,
		"dataChangeLastModifiedBy": args.Operator,
	}
	resp, err := client.R().
		SetHeader("Authorization", args.Token).
		SetHeader("Content-Type", "application/json;charset=UTF-8").
		SetBody(body).
		Put(url)
	if err != nil {
		return nil, fmt.Errorf("apollo update item failed: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("apollo update item returned %d: %s", resp.StatusCode(), redact(resp.String(), args.Token))
	}
	return sonic.Marshal(map[string]any{"success": true, "action": "updated"})
}

func (p *Apollo) configDelete(params json.RawMessage, _ json.RawMessage) (json.RawMessage, error) {
	var args ConfigDeleteArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse config.delete params: %w", err)
	}
	p.applyConnDefaults(&args.PortalURL, &args.Token)
	if err := requireFields("config.delete", map[string]string{
		"portal_url": args.PortalURL,
		"app_id":     args.AppID,
		"env":        args.Env,
		"key":        args.Key,
		"operator":   args.Operator,
	}); err != nil {
		return nil, err
	}
	fillClusterNamespace(&args.Cluster, &args.Namespace)

	url := p.itemURL(args.PortalURL, args.Env, args.AppID, args.Cluster, args.Namespace, args.Key)
	resp, err := p.newClient().R().
		SetHeader("Authorization", args.Token).
		SetQueryParam("operator", args.Operator).
		Delete(url)
	if err != nil {
		return nil, fmt.Errorf("apollo request failed: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("apollo returned %d: %s", resp.StatusCode(), resp.String())
	}
	return sonic.Marshal(map[string]any{"success": true})
}

func (p *Apollo) configRelease(params json.RawMessage, _ json.RawMessage) (json.RawMessage, error) {
	var args ConfigReleaseArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse config.release params: %w", err)
	}
	p.applyConnDefaults(&args.PortalURL, &args.Token)
	if err := requireFields("config.release", map[string]string{
		"portal_url":    args.PortalURL,
		"app_id":        args.AppID,
		"env":           args.Env,
		"release_title": args.ReleaseTitle,
		"released_by":   args.ReleasedBy,
	}); err != nil {
		return nil, err
	}
	fillClusterNamespace(&args.Cluster, &args.Namespace)

	url := fmt.Sprintf("%s/openapi/v1/envs/%s/apps/%s/clusters/%s/namespaces/%s/releases",
		strings.TrimRight(args.PortalURL, "/"), args.Env, args.AppID, args.Cluster, args.Namespace)

	body := map[string]any{
		"releaseTitle":   args.ReleaseTitle,
		"releasedBy":     args.ReleasedBy,
		"releaseComment": args.Comment,
	}
	resp, err := p.newClient().R().
		SetHeader("Authorization", args.Token).
		SetHeader("Content-Type", "application/json;charset=UTF-8").
		SetBody(body).
		Post(url)
	if err != nil {
		return nil, fmt.Errorf("apollo release request failed: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("apollo release returned %d: %s", resp.StatusCode(), redact(resp.String(), args.Token))
	}

	var data map[string]any
	if err := sonic.Unmarshal(resp.Body(), &data); err != nil {
		return sonic.Marshal(map[string]any{"success": true})
	}
	return sonic.Marshal(map[string]any{"success": true, "data": data})
}

func (p *Apollo) namespaceImport(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	var args NamespaceImportArgs
	if err := sonic.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("failed to parse namespace.import params: %w", err)
	}
	p.applyConnDefaults(&args.PortalURL, &args.Token)
	if err := requireFields("namespace.import", map[string]string{
		"portal_url": args.PortalURL,
		"app_id":     args.AppID,
		"env":        args.Env,
		"file":       args.File,
		"operator":   args.Operator,
	}); err != nil {
		return nil, err
	}
	fillClusterNamespace(&args.Cluster, &args.Namespace)
	if args.Format == "" {
		args.Format = inferFormat(args.File)
	}

	workspace := parseWorkspace(opts)
	data, err := readWorkspaceFile(workspace, args.File)
	if err != nil {
		return nil, fmt.Errorf("read import file: %w", err)
	}

	items, err := parseConfigFile(data, args.Format)
	if err != nil {
		return nil, fmt.Errorf("parse config file %q (format=%s): %w", args.File, args.Format, err)
	}
	if len(items) == 0 {
		return sonic.Marshal(map[string]any{"success": true, "imported": 0})
	}

	client := p.newClient()
	itemsURL := p.itemsURL(args.PortalURL, args.Env, args.AppID, args.Cluster, args.Namespace)
	imported := 0
	var errors []string

	for key, value := range items {
		itemURL := p.itemURL(args.PortalURL, args.Env, args.AppID, args.Cluster, args.Namespace, key)
		getResp, err := client.R().
			SetHeader("Authorization", args.Token).
			Get(itemURL)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: request failed: %v", key, err))
			continue
		}

		var body map[string]any
		if getResp.StatusCode() == http.StatusNotFound {
			body = map[string]any{
				"key":                 key,
				"value":               value,
				"dataChangeCreatedBy": args.Operator,
			}
			resp, err := client.R().
				SetHeader("Authorization", args.Token).
				SetHeader("Content-Type", "application/json;charset=UTF-8").
				SetBody(body).
				Post(itemsURL)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: create failed: %v", key, err))
				continue
			}
			if resp.IsError() {
				errors = append(errors, fmt.Sprintf("%s: create returned %d", key, resp.StatusCode()))
				continue
			}
		} else {
			body = map[string]any{
				"key":                      key,
				"value":                    value,
				"dataChangeLastModifiedBy": args.Operator,
			}
			resp, err := client.R().
				SetHeader("Authorization", args.Token).
				SetHeader("Content-Type", "application/json;charset=UTF-8").
				SetBody(body).
				Put(itemURL)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: update failed: %v", key, err))
				continue
			}
			if resp.IsError() {
				errors = append(errors, fmt.Sprintf("%s: update returned %d", key, resp.StatusCode()))
				continue
			}
		}
		imported++
	}

	result := map[string]any{
		"success":  len(errors) == 0,
		"imported": imported,
		"total":    len(items),
	}
	if len(errors) > 0 {
		result["errors"] = errors
	}
	return sonic.Marshal(result)
}

// ---------------------------------------------------------------------------
// URL builders
// ---------------------------------------------------------------------------

func (p *Apollo) itemURL(portalURL, env, appID, cluster, namespace, key string) string {
	return fmt.Sprintf("%s/openapi/v1/envs/%s/apps/%s/clusters/%s/namespaces/%s/items/%s",
		strings.TrimRight(portalURL, "/"), env, appID, cluster, namespace, key)
}

func (p *Apollo) itemsURL(portalURL, env, appID, cluster, namespace string) string {
	return fmt.Sprintf("%s/openapi/v1/envs/%s/apps/%s/clusters/%s/namespaces/%s/items",
		strings.TrimRight(portalURL, "/"), env, appID, cluster, namespace)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (p *Apollo) applyConnDefaults(portalURL, token *string) {
	if *portalURL == "" {
		*portalURL = p.cfg.PortalURL
	}
	if *token == "" {
		*token = p.cfg.Token
	}
}

func (p *Apollo) newClient() *resty.Client {
	return resty.New().SetTimeout(time.Duration(p.cfg.Timeout) * time.Second)
}

func fillClusterNamespace(cluster, namespace *string) {
	if *cluster == "" {
		*cluster = "default"
	}
	if *namespace == "" {
		*namespace = "application"
	}
}

func requireFields(action string, fields map[string]string) error {
	for name, val := range fields {
		if val == "" {
			return fmt.Errorf("%s is required for %s", name, action)
		}
	}
	return nil
}

func resolveValue(value, valueFile string, opts json.RawMessage) (string, error) {
	if valueFile == "" {
		return value, nil
	}
	workspace := parseWorkspace(opts)
	data, err := readWorkspaceFile(workspace, valueFile)
	if err != nil {
		return "", fmt.Errorf("read value_file: %w", err)
	}
	return string(data), nil
}

// ---------------------------------------------------------------------------
// Config file parsing (for namespace.import)
// ---------------------------------------------------------------------------

// parseConfigFile parses a configuration file into key-value pairs.
func parseConfigFile(data []byte, format string) (map[string]string, error) {
	switch strings.ToLower(format) {
	case "properties":
		return parseProperties(data)
	case "yaml", "yml":
		return parseYAML(data)
	case "json":
		return parseJSON(data)
	default:
		return nil, fmt.Errorf("unsupported format %q, expected properties/yaml/json", format)
	}
}

func parseProperties(data []byte) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		idx := strings.IndexAny(line, "=:")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key != "" {
			result[key] = val
		}
	}
	return result, scanner.Err()
}

func parseYAML(data []byte) (map[string]string, error) {
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	flattenMap("", m, result)
	return result, nil
}

func parseJSON(data []byte) (map[string]string, error) {
	var m map[string]any
	if err := sonic.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	flattenMap("", m, result)
	return result, nil
}

// flattenMap recursively flattens a nested map into dotted key-value pairs.
func flattenMap(prefix string, m map[string]any, out map[string]string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch val := v.(type) {
		case map[string]any:
			flattenMap(key, val, out)
		default:
			out[key] = fmt.Sprintf("%v", val)
		}
	}
}

func inferFormat(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".properties":
		return "properties"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	default:
		return "properties"
	}
}

// ---------------------------------------------------------------------------
// Shared file-reading utilities (mirrored from nacos plugin)
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
	plugin.Register(NewApollo())
}
