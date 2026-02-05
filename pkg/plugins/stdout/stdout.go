// Copyright 2025 Arcentra Team
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

package stdout

import (
	"encoding/json"
	"fmt"
	"os"
	"text/template"
	"time"

	"github.com/arcentrix/arcentra/pkg/plugin"
	"github.com/bytedance/sonic"
)

// StdoutConfig is the plugin's configuration structure (can be passed from host via Init)
type StdoutConfig struct {
	// Prefix to display when printing
	Prefix string `json:"prefix"`
	// Whether to output as JSON (message will be json.Marshal'd in Send)
	JSON bool `json:"json"`
}

// Each plugin maintains its own action and args structures

// SendArgs contains arguments for sending notifications
type SendArgs struct {
	Message json.RawMessage `json:"message"`
	Opts    json.RawMessage `json:"opts"`
}

// SendTemplateArgs contains arguments for sending template notifications
type SendTemplateArgs struct {
	Template string          `json:"template"`
	Data     json.RawMessage `json:"data"`
	Opts     json.RawMessage `json:"opts"`
}

// SendBatchArgs contains arguments for sending batch notifications
type SendBatchArgs struct {
	Messages []json.RawMessage `json:"messages"`
	Opts     json.RawMessage   `json:"opts"`
}

// Stdout implements the plugin
type Stdout struct {
	*plugin.PluginBase
	name        string
	description string
	version     string
	cfg         StdoutConfig
}

// Action definitions - maintains action names and descriptions
var (
	actions = map[string]string{
		"send":          "Send a notification message",
		"send.template": "Send a notification using template",
		"send.batch":    "Send batch notifications",
	}
)

// NewStdout creates a new stdout plugin instance
func NewStdout() *Stdout {
	p := &Stdout{
		PluginBase:  plugin.NewPluginBase(),
		name:        "stdout",
		description: "A simple plugin that prints messages to stdout",
		version:     "1.0.0",
	}

	// Register actions using Action Registry
	p.registerActions()
	return p
}

// registerActions registers all actions for this plugin
// Actions are maintained in the actions map above for easy management
func (p *Stdout) registerActions() {
	// Register "send" action
	if err := p.Registry().RegisterFunc("send", actions["send"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		var sendParams SendArgs
		if err := sonic.Unmarshal(params, &sendParams); err != nil {
			return nil, fmt.Errorf("failed to parse send params: %w", err)
		}
		if err := p.Send(sendParams.Message, sendParams.Opts); err != nil {
			return nil, err
		}
		return sonic.Marshal(map[string]string{"status": "sent"})
	}); err != nil {
		return
	}

	// Register "send.template" action
	if err := p.Registry().RegisterFunc("send.template", actions["send.template"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		var tplParams SendTemplateArgs
		if err := sonic.Unmarshal(params, &tplParams); err != nil {
			return nil, fmt.Errorf("failed to parse template params: %w", err)
		}
		if err := p.SendTemplate(tplParams.Template, tplParams.Data, tplParams.Opts); err != nil {
			return nil, err
		}
		return sonic.Marshal(map[string]string{"status": "sent"})
	}); err != nil {
		return
	}

	// Register "send.batch" action
	if err := p.Registry().RegisterFunc("send.batch", actions["send.batch"], func(params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
		var batchParams SendBatchArgs
		if err := sonic.Unmarshal(params, &batchParams); err != nil {
			return nil, fmt.Errorf("failed to parse batch params: %w", err)
		}
		for _, msg := range batchParams.Messages {
			if err := p.Send(msg, batchParams.Opts); err != nil {
				return nil, fmt.Errorf("failed to send batch message: %w", err)
			}
		}
		return sonic.Marshal(map[string]string{"status": "sent", "count": fmt.Sprintf("%d", len(batchParams.Messages))})
	}); err != nil {
		return
	}
}

// Name returns the plugin name
func (p *Stdout) Name() string {
	return p.name
}

// Description returns the plugin description
func (p *Stdout) Description() string {
	return p.description
}

// Version returns the plugin version
func (p *Stdout) Version() string {
	return p.version
}

// Type returns the plugin type
func (p *Stdout) Type() plugin.PluginType {
	return plugin.TypeNotify
}

// Author returns the plugin author
func (p *Stdout) Author() string {
	return "Arcentra Team"
}

// Repository returns the plugin repository
func (p *Stdout) Repository() string {
	return "https://github.com/arcentrix/arcentra"
}

// Init initializes the plugin
func (p *Stdout) Init(config json.RawMessage) error {
	if len(config) > 0 {
		if err := sonic.Unmarshal(config, &p.cfg); err != nil {
			// Use default config if unmarshal fails
			p.cfg = StdoutConfig{}
		}
	}
	return nil
}

// Cleanup cleans up the plugin
func (p *Stdout) Cleanup() error {
	return nil
}

// Execute executes custom actions using Action Registry
// All actions are registered in registerActions() and routed through the registry
func (p *Stdout) Execute(action string, params json.RawMessage, opts json.RawMessage) (json.RawMessage, error) {
	return p.PluginBase.Execute(action, params, opts)
}

// Send sends a notification message
func (p *Stdout) Send(message json.RawMessage, opts json.RawMessage) error {
	now := time.Now().Format(time.RFC3339)

	var line string
	if p.cfg.JSON {
		// Already in JSON format
		line = string(message)
	} else {
		// Try to parse as string first
		var msgStr string
		if err := sonic.Unmarshal(message, &msgStr); err == nil {
			line = msgStr
		} else {
			// If not a string, output as compact JSON
			line = string(message)
		}
	}

	prefix := p.cfg.Prefix
	if prefix != "" {
		prefix = "[" + prefix + "] "
	}

	_, err := fmt.Fprintf(os.Stdout, "%s%s | %s\n", prefix, now, line)
	return err
}

// SendTemplate sends a notification using a template
func (p *Stdout) SendTemplate(tpl string, data json.RawMessage, opts json.RawMessage) error {
	// Parse template
	t, err := template.New("stdout").Parse(tpl)
	if err != nil {
		return fmt.Errorf("stdout parse template: %w", err)
	}

	// Unmarshal data
	var templateData any
	if len(data) > 0 {
		if err := sonic.Unmarshal(data, &templateData); err != nil {
			return fmt.Errorf("stdout unmarshal data: %w", err)
		}
	}

	prefix := p.cfg.Prefix
	if prefix != "" {
		prefix = "[" + prefix + "] "
	}
	_, _ = fmt.Fprintf(os.Stdout, "%s%s | ", prefix, time.Now().Format(time.RFC3339))

	if err := t.Execute(os.Stdout, templateData); err != nil {
		return fmt.Errorf("stdout execute template: %w", err)
	}
	_, _ = fmt.Fprintln(os.Stdout) // Newline
	return nil
}

// init registers the plugin
func init() {
	plugin.Register(NewStdout())
}
