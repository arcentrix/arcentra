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

package nova

import (
	"encoding/json"
	"testing"
)

// Test message struct for JSON/Sonic codec tests
type testMessage struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

func TestJSONCodec_Encode(t *testing.T) {
	codec := NewJSONCodec()
	msg := testMessage{
		ID:    1,
		Name:  "test",
		Value: "value",
	}

	data, err := codec.Encode(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(data) == 0 {
		t.Error("expected encoded data to be non-empty")
	}

	// Verify it's valid JSON
	var decoded testMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("encoded data is not valid JSON: %v", err)
	}

	if decoded.ID != msg.ID || decoded.Name != msg.Name || decoded.Value != msg.Value {
		t.Error("decoded message doesn't match original")
	}
}

func TestJSONCodec_Decode(t *testing.T) {
	codec := NewJSONCodec()
	original := testMessage{
		ID:    1,
		Name:  "test",
		Value: "value",
	}

	data, _ := json.Marshal(original)
	var decoded testMessage

	err := codec.Decode(data, &decoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if decoded.ID != original.ID || decoded.Name != original.Name || decoded.Value != original.Value {
		t.Error("decoded message doesn't match original")
	}
}

func TestJSONCodec_Format(t *testing.T) {
	codec := NewJSONCodec()
	if codec.Format() != MessageFormatJSON {
		t.Errorf("expected format to be MessageFormatJSON, got %s", codec.Format())
	}
}

func TestSonicCodec_Encode(t *testing.T) {
	codec := NewSonicCodec()
	msg := testMessage{
		ID:    1,
		Name:  "test",
		Value: "value",
	}

	data, err := codec.Encode(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(data) == 0 {
		t.Error("expected encoded data to be non-empty")
	}
}

func TestSonicCodec_Decode(t *testing.T) {
	codec := NewSonicCodec()
	original := testMessage{
		ID:    1,
		Name:  "test",
		Value: "value",
	}

	data, _ := codec.Encode(original)
	var decoded testMessage

	err := codec.Decode(data, &decoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if decoded.ID != original.ID || decoded.Name != original.Name || decoded.Value != original.Value {
		t.Error("decoded message doesn't match original")
	}
}

func TestSonicCodec_Format(t *testing.T) {
	codec := NewSonicCodec()
	if codec.Format() != MessageFormatSonic {
		t.Errorf("expected format to be MessageFormatSonic, got %s", codec.Format())
	}
}

func TestBlobCodec_Encode(t *testing.T) {
	codec := NewBlobCodec()

	tests := []struct {
		name    string
		msg     any
		wantErr bool
	}{
		{
			name:    "byte slice",
			msg:     []byte("test data"),
			wantErr: false,
		},
		{
			name:    "pointer to byte slice",
			msg:     func() *[]byte { data := []byte("test data"); return &data }(),
			wantErr: false,
		},
		{
			name:    "string (unsupported)",
			msg:     "test",
			wantErr: true,
		},
		{
			name:    "int (unsupported)",
			msg:     123,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := codec.Encode(tt.msg)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(data) == 0 {
				t.Error("expected encoded data to be non-empty")
			}
		})
	}
}

func TestBlobCodec_Decode(t *testing.T) {
	codec := NewBlobCodec()
	testData := []byte("test data")

	tests := []struct {
		name    string
		target  any
		wantErr bool
	}{
		{
			name:    "pointer to byte slice",
			target:  func() *[]byte { var data []byte; return &data }(),
			wantErr: false,
		},
		{
			name:    "byte slice (not pointer)",
			target:  []byte{},
			wantErr: true,
		},
		{
			name:    "string (unsupported)",
			target:  new(string),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := codec.Decode(testData, tt.target)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if target, ok := tt.target.(*[]byte); ok {
				if string(*target) != string(testData) {
					t.Errorf("expected decoded data to be '%s', got '%s'", string(testData), string(*target))
				}
			}
		})
	}
}

func TestBlobCodec_Format(t *testing.T) {
	codec := NewBlobCodec()
	if codec.Format() != MessageFormatBlob {
		t.Errorf("expected format to be MessageFormatBlob, got %s", codec.Format())
	}
}

// Note: Implementing a complete protoreflect.Message for testing is complex
// because ProtoMethods() must return *protoreflect.methods which is unexported.
// For testing purposes, we'll focus on testing the codec's error handling
// for non-proto messages. In production, you would use actual protobuf-generated message types.

func TestProtobufCodec_Encode(t *testing.T) {
	codec := NewProtobufCodec()

	tests := []struct {
		name    string
		msg     any
		wantErr bool
	}{
		{
			name:    "non-proto message",
			msg:     testMessage{ID: 1, Name: "test", Value: "value"},
			wantErr: true,
		},
		{
			name:    "nil",
			msg:     nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := codec.Encode(tt.msg)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestProtobufCodec_Decode(t *testing.T) {
	codec := NewProtobufCodec()
	testData := []byte{0x08, 0x01}

	tests := []struct {
		name    string
		target  any
		wantErr bool
	}{
		{
			name:    "non-proto message",
			target:  &testMessage{},
			wantErr: true,
		},
		{
			name:    "nil",
			target:  nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := codec.Decode(testData, tt.target)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestProtobufCodec_Format(t *testing.T) {
	codec := NewProtobufCodec()
	if codec.Format() != MessageFormatProtobuf {
		t.Errorf("expected format to be MessageFormatProtobuf, got %s", codec.Format())
	}
}

func TestNewMessageCodec(t *testing.T) {
	tests := []struct {
		name     string
		format   MessageFormat
		wantErr  bool
		wantType string
	}{
		{
			name:     "JSON format",
			format:   MessageFormatJSON,
			wantErr:  false,
			wantType: "*nova.JSONCodec",
		},
		{
			name:     "Sonic format",
			format:   MessageFormatSonic,
			wantErr:  false,
			wantType: "*nova.SonicCodec",
		},
		{
			name:     "Blob format",
			format:   MessageFormatBlob,
			wantErr:  false,
			wantType: "*nova.BlobCodec",
		},
		{
			name:     "Protobuf format",
			format:   MessageFormatProtobuf,
			wantErr:  false,
			wantType: "*nova.ProtobufCodec",
		},
		{
			name:    "unsupported format",
			format:  MessageFormat("unknown"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codec, err := NewMessageCodec(tt.format)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if codec == nil {
				t.Error("expected codec to be non-nil")
				return
			}

			// Check type by checking Format() method
			if codec.Format() != tt.format {
				t.Errorf("expected format to be %s, got %s", tt.format, codec.Format())
			}
		})
	}
}

func TestDefaultMessageCodec(t *testing.T) {
	if DefaultMessageCodec == nil {
		t.Error("expected DefaultMessageCodec to be non-nil")
		return
	}

	if DefaultMessageCodec.Format() != MessageFormatJSON {
		t.Errorf("expected DefaultMessageCodec format to be MessageFormatJSON, got %s", DefaultMessageCodec.Format())
	}
}
