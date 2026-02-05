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

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/template"
	"time"

	"github.com/bytedance/sonic"
)

// StdoutConfig contains configuration for stdout builtin
type StdoutConfig struct {
	Prefix string `json:"prefix"` // Prefix to display when printing
	JSON   bool   `json:"json"`   // Whether to output as JSON
}

// StdoutSendArgs contains arguments for sending notifications
type StdoutSendArgs struct {
	Message json.RawMessage `json:"message"`
	Config  *StdoutConfig   `json:"config,omitempty"`
}

// StdoutSendTemplateArgs contains arguments for sending template notifications
type StdoutSendTemplateArgs struct {
	Template string          `json:"template"`
	Data     json.RawMessage `json:"data"`
	Config   *StdoutConfig   `json:"config,omitempty"`
}

// StdoutSendBatchArgs contains arguments for sending batch notifications
type StdoutSendBatchArgs struct {
	Messages []json.RawMessage `json:"messages"`
	Config   *StdoutConfig     `json:"config,omitempty"`
}

// handleStdoutSend handles stdout send action
func (m *Manager) handleStdoutSend(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var sendParams StdoutSendArgs
	if err := sonic.Unmarshal(params, &sendParams); err != nil {
		return nil, fmt.Errorf("failed to parse send params: %w", err)
	}

	config := sendParams.Config
	if config == nil {
		config = &StdoutConfig{}
	}

	if err := m.stdoutSend(sendParams.Message, config); err != nil {
		return nil, err
	}

	return sonic.Marshal(map[string]string{"status": "sent"})
}

// handleStdoutSendTemplate handles stdout send.template action
func (m *Manager) handleStdoutSendTemplate(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var tplParams StdoutSendTemplateArgs
	if err := sonic.Unmarshal(params, &tplParams); err != nil {
		return nil, fmt.Errorf("failed to parse template params: %w", err)
	}

	config := tplParams.Config
	if config == nil {
		config = &StdoutConfig{}
	}

	if err := m.stdoutSendTemplate(tplParams.Template, tplParams.Data, config); err != nil {
		return nil, err
	}

	return sonic.Marshal(map[string]string{"status": "sent"})
}

// handleStdoutSendBatch handles stdout send.batch action
func (m *Manager) handleStdoutSendBatch(ctx context.Context, params json.RawMessage, opts *Options) (json.RawMessage, error) {
	var batchParams StdoutSendBatchArgs
	if err := sonic.Unmarshal(params, &batchParams); err != nil {
		return nil, fmt.Errorf("failed to parse batch params: %w", err)
	}

	config := batchParams.Config
	if config == nil {
		config = &StdoutConfig{}
	}

	for i, msg := range batchParams.Messages {
		if err := m.stdoutSend(msg, config); err != nil {
			return nil, fmt.Errorf("failed to send batch message at index %d: %w", i, err)
		}
	}

	return sonic.Marshal(map[string]any{
		"status": "sent",
		"count":  len(batchParams.Messages),
	})
}

// stdoutSend sends a notification message to stdout
func (m *Manager) stdoutSend(message json.RawMessage, config *StdoutConfig) error {
	now := time.Now().Format(time.RFC3339)

	var line string
	if config.JSON {
		line = string(message)
	} else {
		var msgStr string
		if err := sonic.Unmarshal(message, &msgStr); err == nil {
			line = msgStr
		} else {
			line = string(message)
		}
	}

	prefix := config.Prefix
	if prefix != "" {
		prefix = "[" + prefix + "] "
	}

	_, err := fmt.Fprintf(os.Stdout, "%s%s | %s\n", prefix, now, line)
	return err
}

// stdoutSendTemplate sends a notification using a template
func (m *Manager) stdoutSendTemplate(tpl string, data json.RawMessage, config *StdoutConfig) error {
	t, err := template.New("stdout").Parse(tpl)
	if err != nil {
		return fmt.Errorf("stdout parse template: %w", err)
	}

	var templateData any
	if len(data) > 0 {
		if err := sonic.Unmarshal(data, &templateData); err != nil {
			return fmt.Errorf("stdout unmarshal data: %w", err)
		}
	}

	prefix := config.Prefix
	if prefix != "" {
		prefix = "[" + prefix + "] "
	}
	_, _ = fmt.Fprintf(os.Stdout, "%s%s | ", prefix, time.Now().Format(time.RFC3339))

	if err := t.Execute(os.Stdout, templateData); err != nil {
		return fmt.Errorf("stdout execute template: %w", err)
	}
	_, _ = fmt.Fprintln(os.Stdout)
	return nil
}
