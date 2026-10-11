// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package outbox

import (
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	// MaxEventPayloadBytes 限制单个事件规范化 JSON 载荷，和网关接收限制一致
	MaxEventPayloadBytes = 1024 * 1024
	// MaxGatewayMessageBytes 限制单条网关消息，保持在 gRPC 默认接收上限内
	MaxGatewayMessageBytes = 4 * 1024 * 1024
	// MaxGatewayBatchEvents 限制单条网关消息内的事件数量
	MaxGatewayBatchEvents = 1000
)

// ErrEventPayloadTooLarge 表示事件超过传输限制，必须在分配 WAL 序号之前拒绝
var ErrEventPayloadTooLarge = errors.New("outbox: event payload exceeds gateway limits")

// ValidateEventPayload 检查事件经过 protobuf 往返后的 JSON 和消息大小，防止永久阻塞发件箱
func ValidateEventPayload(payload map[string]any) error {
	if payload == nil {
		return errors.New("outbox: event payload must be a JSON object")
	}
	value, err := structpb.NewStruct(payload)
	if err != nil {
		return fmt.Errorf("outbox: invalid event payload: %w", err)
	}
	encoded, err := sonic.Marshal(value.AsMap())
	if err != nil {
		return fmt.Errorf("outbox: invalid event JSON: %w", err)
	}
	// Reserve space for the bounded event IDs, metadata and batch envelope.
	if len(encoded) > MaxEventPayloadBytes || proto.Size(value) > MaxGatewayMessageBytes-4096 {
		return ErrEventPayloadTooLarge
	}
	return nil
}
