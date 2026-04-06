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

package template

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Engine handles template rendering.
type Engine struct {
	funcMap template.FuncMap
}

func NewTemplateEngine() *Engine {
	titleCaser := cases.Title(language.English)
	funcMap := template.FuncMap{
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"title": titleCaser.String,
		"trim":  strings.TrimSpace,
	}
	return &Engine{funcMap: funcMap}
}

func (e *Engine) Render(tmplContent string, data map[string]interface{}) (string, error) {
	tmpl, err := template.New("notification").Funcs(e.funcMap).Parse(tmplContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}
	return buf.String(), nil
}

func (e *Engine) RenderSimple(tmplContent string, data map[string]interface{}) string {
	result := tmplContent
	for key, value := range data {
		placeholder := fmt.Sprintf("{{%s}}", key)
		result = strings.ReplaceAll(result, placeholder, fmt.Sprintf("%v", value))
	}
	return result
}

func (e *Engine) ValidateTemplate(tmplContent string) error {
	_, err := template.New("validation").Funcs(e.funcMap).Parse(tmplContent)
	return err
}

func (e *Engine) ExtractVariables(tmplContent string) []string {
	variables := make(map[string]bool)
	parts := strings.Split(tmplContent, "{{")
	for i := 1; i < len(parts); i++ {
		endIdx := strings.Index(parts[i], "}}")
		if endIdx > 0 {
			varName := strings.TrimSpace(parts[i][:endIdx])
			varName = strings.TrimPrefix(varName, ".")
			if !strings.Contains(varName, " ") && varName != "" {
				variables[varName] = true
			}
		}
	}

	result := make([]string, 0, len(variables))
	for v := range variables {
		result = append(result, v)
	}
	return result
}
