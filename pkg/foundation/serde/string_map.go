// Copyright 2026 Arcentra Authors.
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

package serde

import (
	"strings"

	"github.com/bytedance/sonic"
	"google.golang.org/protobuf/types/known/structpb"
)

// MarshalStringMap serializes map[string]string to JSON string.
func MarshalStringMap(data map[string]string) string {
	if len(data) == 0 {
		return ""
	}
	raw, err := sonic.Marshal(data)
	if err != nil {
		return ""
	}
	return string(raw)
}

// UnmarshalStringMap deserializes JSON string to map[string]string.
func UnmarshalStringMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}
	}
	out := map[string]string{}
	if err := sonic.UnmarshalString(raw, &out); err != nil {
		return map[string]string{}
	}
	return out
}

// StringMapToStruct converts map[string]string to protobuf Struct.
func StringMapToStruct(data map[string]string) *structpb.Struct {
	if len(data) == 0 {
		return nil
	}
	obj := make(map[string]any, len(data))
	for k, v := range data {
		obj[k] = v
	}
	pb, err := structpb.NewStruct(obj)
	if err != nil {
		return nil
	}
	return pb
}
