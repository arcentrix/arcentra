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

package tool

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"text/template"

	"github.com/gofiber/fiber/v2"
)

const (
	// I18nLocalizerKey is the key name for the localizer in Fiber Context.
	// If i18n middleware (such as fiberi18n) is used, the localizer will be stored under this key.
	I18nLocalizerKey = "i18n"
)

// Localizer is the interface for localization.
// Supports multiple i18n library implementations, such as go-i18n, fiberi18n, etc.
type Localizer interface {
	// Localize returns the localized message based on the message ID.
	Localize(messageID string) (string, error)
}

// GetLocalizedMessage retrieves a localized message and applies template data.
// If i18n middleware is configured, it will get the translated message from the localizer.
// If not configured, it returns the original messageId.
// templateData is used to replace template variables in the message,
// e.g., {{.Name}} will be replaced with templateData["Name"].
func GetLocalizedMessage(c *fiber.Ctx, messageId string, templateData map[string]string) string {
	message := getLocalizedMessage(c, messageId)
	return applyTemplate(message, templateData)
}

// GetLocalized retrieves a localized message without template data.
// If i18n middleware is configured, it will get the translated message from the localizer.
// If not configured, it returns the original messageId.
func GetLocalized(c *fiber.Ctx, messageId string) string {
	return GetLocalizedMessage(c, messageId, nil)
}

// getLocalizedMessage retrieves a localized message from context.
// Supports multiple i18n library implementation methods.
func getLocalizedMessage(c *fiber.Ctx, messageId string) string {
	localizerValue := c.Locals(I18nLocalizerKey)
	if localizerValue == nil {
		return messageId
	}

	// Try to get localized message through Localizer interface
	if localizer, ok := localizerValue.(Localizer); ok {
		if msg, err := localizer.Localize(messageId); err == nil && msg != "" {
			return msg
		}
	}

	// Try to call common i18n library methods using reflection
	// Supports go-i18n's Localizer.Localize method
	if msg := tryReflectLocalize(localizerValue, messageId); msg != "" {
		return msg
	}

	// If all attempts fail, return the original messageId
	return messageId
}

// tryReflectLocalize attempts to call localization methods using reflection.
// Supports go-i18n and other libraries' Localize methods.
func tryReflectLocalize(localizer any, messageId string) string {
	v := reflect.ValueOf(localizer)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	// Try to call Localize method (go-i18n signature)
	localizeMethod := v.MethodByName("Localize")
	if !localizeMethod.IsValid() {
		return ""
	}

	// Try different parameter types
	// go-i18n's Localize accepts *i18n.LocalizeConfig
	// We try to create a compatible config object
	configType := localizeMethod.Type()
	if configType.NumIn() == 0 {
		return ""
	}

	// Create config parameter
	paramType := configType.In(0)
	var param reflect.Value

	// Try to create *i18n.LocalizeConfig type parameter
	if paramType.Kind() == reflect.Ptr {
		elemType := paramType.Elem()
		if elemType.Kind() == reflect.Struct {
			// Create struct and set MessageID field
			config := reflect.New(elemType)
			if msgIDField := config.Elem().FieldByName("MessageID"); msgIDField.IsValid() && msgIDField.CanSet() {
				msgIDField.SetString(messageId)
				param = config
			} else {
				return ""
			}
		} else {
			return ""
		}
	} else {
		return ""
	}

	// Call method
	results := localizeMethod.Call([]reflect.Value{param})
	if len(results) == 0 {
		return ""
	}

	// Get return value (usually string, error)
	if len(results) >= 1 {
		resultValue := results[0]
		if resultValue.Kind() == reflect.String {
			msg := resultValue.String()
			// Check for errors
			if len(results) >= 2 {
				errValue := results[1]
				if !errValue.IsNil() {
					return ""
				}
			}
			return msg
		}
	}

	return ""
}

// applyTemplate applies template data to the message string.
// Supports {{.Key}} format template variable replacement.
func applyTemplate(message string, templateData map[string]string) string {
	if len(templateData) == 0 {
		return message
	}

	// If there are no template variables, return directly
	if !strings.Contains(message, "{{") {
		return message
	}

	// Create template
	tmpl, err := template.New("message").Parse(message)
	if err != nil {
		// If template parsing fails, try simple string replacement as fallback
		return simpleReplace(message, templateData)
	}

	// Convert map[string]string to map[string]interface{} for template use
	data := make(map[string]any)
	for k, v := range templateData {
		data[k] = v
	}

	// Execute template replacement
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		// If template execution fails, try simple string replacement as fallback
		return simpleReplace(message, templateData)
	}

	return buf.String()
}

// simpleReplace is a simple string replacement fallback.
// Replaces {{.Key}} with corresponding values.
func simpleReplace(message string, templateData map[string]string) string {
	result := message
	for key, value := range templateData {
		// Replace {{.Key}} format
		placeholder := fmt.Sprintf("{{.%s}}", key)
		result = strings.ReplaceAll(result, placeholder, value)
		// Also replace {{Key}} format (without dot)
		placeholder2 := fmt.Sprintf("{{%s}}", key)
		result = strings.ReplaceAll(result, placeholder2, value)
	}
	return result
}
