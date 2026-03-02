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

package spec

import (
	"fmt"
	"strings"

	pipelinev1 "github.com/arcentrix/arcentra/api/pipeline/v1"
	"github.com/bytedance/sonic"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func ParseContentToProto(content string, format pipelinev1.SpecFormat) (*pipelinev1.Spec, error) {
	jsonContent, err := normalizeSpecToJSON(content, format)
	if err != nil {
		return nil, err
	}
	var pl pipelinev1.Spec
	if err := protojson.Unmarshal([]byte(jsonContent), &pl); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}
	return &pl, nil
}

func ValidateProto(sp *pipelinev1.Spec) error {
	if sp == nil {
		return fmt.Errorf("spec is nil")
	}
	return nil
}

func MarshalProtoByFormat(sp *pipelinev1.Spec, format pipelinev1.SpecFormat, path string) (string, error) {
	if sp == nil {
		return "", fmt.Errorf("spec is nil")
	}
	actual := format
	if actual == pipelinev1.SpecFormat_SPEC_FORMAT_UNSPECIFIED {
		if strings.HasSuffix(strings.ToLower(path), ".json") {
			actual = pipelinev1.SpecFormat_SPEC_FORMAT_JSON
		} else {
			actual = pipelinev1.SpecFormat_SPEC_FORMAT_YAML
		}
	}
	switch actual {
	case pipelinev1.SpecFormat_SPEC_FORMAT_JSON:
		raw, err := protojson.Marshal(sp)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	default:
		rawJSON, err := protojson.Marshal(sp)
		if err != nil {
			return "", err
		}
		var obj map[string]any
		if err := sonic.Unmarshal(rawJSON, &obj); err != nil {
			return "", err
		}
		raw, err := yaml.Marshal(obj)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
}

func ProtoToSpec(sp *pipelinev1.Spec) *Pipeline {
	return (*Pipeline)(sp)
}

func SpecToProto(pl *Pipeline) *pipelinev1.Spec {
	return (*pipelinev1.Spec)(pl)
}

func StructToAnyMap(in *structpb.Struct) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	return in.AsMap()
}

func AnyMapToStruct(in map[string]any) *structpb.Struct {
	if len(in) == 0 {
		s, _ := structpb.NewStruct(map[string]any{})
		return s
	}
	s, err := structpb.NewStruct(in)
	if err != nil {
		fallback, _ := structpb.NewStruct(map[string]any{})
		return fallback
	}
	return s
}

func normalizeSpecToJSON(content string, format pipelinev1.SpecFormat) (string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", fmt.Errorf("content is empty")
	}
	switch format {
	case pipelinev1.SpecFormat_SPEC_FORMAT_JSON:
		return trimmed, nil
	case pipelinev1.SpecFormat_SPEC_FORMAT_YAML:
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(trimmed), &obj); err != nil {
			return "", fmt.Errorf("invalid yaml content: %w", err)
		}
		raw, err := sonic.Marshal(obj)
		if err != nil {
			return "", fmt.Errorf("yaml to json failed: %w", err)
		}
		return string(raw), nil
	default:
		if strings.HasPrefix(trimmed, "{") {
			return trimmed, nil
		}
		var obj map[string]any
		if err := yaml.Unmarshal([]byte(trimmed), &obj); err != nil {
			return "", fmt.Errorf("unsupported format or invalid content: %w", err)
		}
		raw, err := sonic.Marshal(obj)
		if err != nil {
			return "", fmt.Errorf("normalize content failed: %w", err)
		}
		return string(raw), nil
	}
}
