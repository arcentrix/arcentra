// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package taskqueue

import (
	"fmt"
	"strings"

	"github.com/arcentrix/arcentra/pkg/nova"
)

// ResolveMessageCodec 校验控制器和 Agent 的任务信封编码，默认使用 Sonic JSON
// 显式 codec 优先；json 和 sonic 使用同一 JSON 协议，两个字段均不得指定不兼容格式
func ResolveMessageCodec(messageFormat, messageCodec string) (nova.MessageCodec, error) {
	format := nova.MessageFormat(strings.ToLower(strings.TrimSpace(messageFormat)))
	codec := nova.MessageFormat(strings.ToLower(strings.TrimSpace(messageCodec)))
	for _, field := range []struct {
		name  string
		value nova.MessageFormat
	}{{"messageFormat", format}, {"messageCodec", codec}} {
		switch field.value {
		case "", nova.MessageFormatJSON, nova.MessageFormatSonic:
		default:
			return nil, fmt.Errorf("taskQueue.%s is incompatible with the Nova task envelope; use json or sonic on Control and Agent", field.name)
		}
	}
	if codec == "" {
		codec = format
	}
	if codec == "" {
		codec = nova.MessageFormatSonic
	}
	return nova.NewMessageCodec(codec)
}

// WrapStartupError 保留启动错误因果链，但公开文本只包含固定操作名，避免泄露 Kafka 凭据
// operation 必须由调用方使用固定阶段名，不能包含配置值或底层错误文本
func WrapStartupError(operation string, cause error) error {
	return &startupError{operation: operation, cause: cause}
}

type startupError struct {
	operation string
	cause     error
}

func (e *startupError) Error() string {
	return e.operation + ": Kafka startup failed; check broker settings"
}

func (e *startupError) Unwrap() error { return e.cause }
