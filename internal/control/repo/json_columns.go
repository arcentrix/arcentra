package repo

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/datatypes"
)

// These columns remain string-valued in the model/API. An omitted map is an
// empty JSON object on insertion; an omitted retry policy is JSON null. This
// does not migrate, reinterpret, or rewrite existing rows.
var pipelineJSONDefaults = map[string]string{"metadata": "{}"}
var pipelineRunJSONDefaults = map[string]string{"env": "{}"}
var jobRunJSONDefaults = map[string]string{"env": "{}", "artifact_references": "{}", "artifact_uris": "{}"}
var stepRunJSONDefaults = map[string]string{"retry_policy": "null", "args": "{}", "env": "{}", "secrets": "{}", "label_selector": "{}"}

func validJSONColumn(column, value, empty string) (string, error) {
	if value == "" {
		return empty, nil
	}
	if !json.Valid([]byte(value)) {
		// Configuration and environment values can contain secrets. Never echo
		// the rejected document in errors or logs.
		return "", fmt.Errorf("invalid JSON for column %s", column)
	}
	return value, nil
}

func normalizeJSONFields(fields map[string]*string, defaults map[string]string) error {
	normalized := make(map[string]string, len(fields))
	for column, field := range fields {
		value, err := validJSONColumn(column, *field, defaults[column])
		if err != nil {
			return err
		}
		normalized[column] = value
	}
	for column, value := range normalized {
		*fields[column] = value
	}
	return nil
}

// Only explicitly submitted JSON columns are normalized. Missing columns are
// not added to a patch, and malformed values never become empty defaults.
func normalizeJSONUpdates(updates map[string]any, defaults map[string]string) (map[string]any, error) {
	result := make(map[string]any, len(updates))
	for key, input := range updates {
		column, empty := "", ""
		for candidate, fallback := range defaults {
			if strings.EqualFold(strings.ReplaceAll(key, "_", ""), strings.ReplaceAll(candidate, "_", "")) {
				column, empty = candidate, fallback
				break
			}
		}
		if column == "" || input == nil {
			result[key] = input
			continue
		}
		var raw string
		switch value := input.(type) {
		case string:
			raw = value
		case []byte:
			raw = string(value)
		case json.RawMessage:
			raw = string(value)
		case datatypes.JSON:
			raw = string(value)
		default:
			return nil, fmt.Errorf("JSON column %s requires an encoded JSON document", column)
		}
		value, err := validJSONColumn(column, raw, empty)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}
