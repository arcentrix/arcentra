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

package nova

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/proto"
)

// MessageFormat 消息格式类型
type MessageFormat string

const (
	MessageFormatJSON     MessageFormat = "json"     // JSON format
	MessageFormatSonic    MessageFormat = "sonic"    // Sonic format (high-performance JSON)
	MessageFormatBlob     MessageFormat = "blob"     // Blob format (binary large object)
	MessageFormatProtobuf MessageFormat = "protobuf" // Protobuf format
)

// MessageCodec is the interface for message encoding and decoding
type MessageCodec interface {
	// Encode encodes a message to bytes
	Encode(msg any) ([]byte, error)

	// Decode decodes bytes to a message
	Decode(data []byte, msg any) error

	// Format returns the message format
	Format() MessageFormat
}

// JSONCodec implements JSON encoding/decoding
type JSONCodec struct{}

// NewJSONCodec creates a new JSON codec
func NewJSONCodec() *JSONCodec {
	return &JSONCodec{}
}

// Encode encodes a message to JSON bytes
func (c *JSONCodec) Encode(msg any) ([]byte, error) {
	return json.Marshal(msg)
}

// Decode decodes JSON bytes to a message
func (c *JSONCodec) Decode(data []byte, msg any) error {
	return json.Unmarshal(data, msg)
}

// Format returns the message format
func (c *JSONCodec) Format() MessageFormat {
	return MessageFormatJSON
}

// SonicCodec implements Sonic encoding/decoding (high-performance JSON)
type SonicCodec struct{}

// NewSonicCodec creates a new Sonic codec
func NewSonicCodec() *SonicCodec {
	return &SonicCodec{}
}

// Encode encodes a message to Sonic JSON bytes
func (c *SonicCodec) Encode(msg any) ([]byte, error) {
	return sonic.Marshal(msg)
}

// Decode decodes Sonic JSON bytes to a message
func (c *SonicCodec) Decode(data []byte, msg any) error {
	return sonic.Unmarshal(data, msg)
}

// Format returns the message format
func (c *SonicCodec) Format() MessageFormat {
	return MessageFormatSonic
}

// BlobCodec implements Blob encoding/decoding
// Blob format stores raw byte data without serialization
type BlobCodec struct{}

// NewBlobCodec creates a new Blob codec
func NewBlobCodec() *BlobCodec {
	return &BlobCodec{}
}

// Encode encodes a message to Blob (returns byte data directly)
func (c *BlobCodec) Encode(msg any) ([]byte, error) {
	// If message is []byte, return directly
	if data, ok := msg.([]byte); ok {
		return data, nil
	}
	// If message is *[]byte, return its value
	if data, ok := msg.(*[]byte); ok {
		return *data, nil
	}
	// Other types are not supported
	return nil, fmt.Errorf("blob codec only supports []byte, got %T", msg)
}

// Decode decodes Blob bytes to a message (returns byte data directly)
func (c *BlobCodec) Decode(data []byte, msg any) error {
	// If target is *[]byte, assign directly
	if target, ok := msg.(*[]byte); ok {
		*target = make([]byte, len(data))
		copy(*target, data)
		return nil
	}
	return fmt.Errorf("blob codec decode target must be *[]byte, got %T", msg)
}

// Format returns the message format
func (c *BlobCodec) Format() MessageFormat {
	return MessageFormatBlob
}

// ProtobufCodec implements Protobuf encoding/decoding
type ProtobufCodec struct{}

// NewProtobufCodec creates a new Protobuf codec
func NewProtobufCodec() *ProtobufCodec {
	return &ProtobufCodec{}
}

// Encode encodes a message to Protobuf bytes
// Message must implement proto.Message interface
func (c *ProtobufCodec) Encode(msg any) ([]byte, error) {
	pbMsg, ok := msg.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("message must implement proto.Message interface")
	}
	return proto.Marshal(pbMsg)
}

// Decode decodes Protobuf bytes to a message
// must implement proto.Message interface
func (c *ProtobufCodec) Decode(data []byte, msg any) error {
	pbMsg, ok := msg.(proto.Message)
	if !ok {
		return fmt.Errorf("message must implement proto.Message interface")
	}
	return proto.Unmarshal(data, pbMsg)
}

// Format returns the message format
func (c *ProtobufCodec) Format() MessageFormat {
	return MessageFormatProtobuf
}

// NewMessageCodec creates a message codec based on the format
func NewMessageCodec(format MessageFormat) (MessageCodec, error) {
	switch format {
	case MessageFormatJSON:
		return NewJSONCodec(), nil
	case MessageFormatSonic:
		return NewSonicCodec(), nil
	case MessageFormatBlob:
		return NewBlobCodec(), nil
	case MessageFormatProtobuf:
		return NewProtobufCodec(), nil
	default:
		return nil, fmt.Errorf("unsupported message format: %s", format)
	}
}

// DefaultMessageCodec is the default message codec (JSON)
var DefaultMessageCodec MessageCodec = NewJSONCodec()
