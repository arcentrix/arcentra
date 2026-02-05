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

package convert

import (
	"github.com/bytedance/sonic"
	"sigs.k8s.io/yaml"
)

// JSONToYAML → YAML
func JSONToYAML(jsonData []byte) ([]byte, error) {
	y, err := yaml.JSONToYAML(jsonData)
	if err != nil {
		return nil, err
	}
	return y, nil
}

// YAMLToJSON → JSON
func YAMLToJSON(yamlData []byte) ([]byte, error) {
	j, err := yaml.YAMLToJSON(yamlData)
	if err != nil {
		return nil, err
	}
	return j, nil
}

// PrettyJSONToYAML JSON → YAML (pretty json as input)
func PrettyJSONToYAML(v any) ([]byte, error) {
	b, err := sonic.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return JSONToYAML(b)
}
