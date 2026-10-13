package router

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"gorm.io/datatypes"
)

// Secrets are write-only over administrative HTTP. ConfiguredFields describes
// presence without ever supplying a placeholder that a client might persist.
type settingView struct {
	model.Setting
	ConfiguredFields []string `json:"configuredFields"`
}
type storageConfigView struct {
	model.StorageConfig
	ConfiguredFields []string `json:"configuredFields"`
}

type identityView struct {
	model.Identity
	ConfiguredFields []string `json:"configuredFields"`
}

func publicIdentity(item *model.Identity) identityView {
	view := identityView{Identity: *item}
	view.Config, view.ConfiguredFields = redactConfig(item.Config, false)
	return view
}

func secretField(name string) bool {
	n := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(name))
	if n == "key" {
		return true
	}
	if n == "tokenurl" {
		return false
	}
	for _, part := range []string{"secret", "password", "token", "credential", "privatekey", "accesskey", "apikey", "salt", "authorization", "authheader", "bearer", "signingkey", "encryptionkey"} {
		if strings.Contains(n, part) {
			return true
		}
	}
	return false
}

func secretURLField(key string, value any) bool {
	text, ok := value.(string)
	return ok && text != "" && (strings.HasSuffix(strings.ToLower(key), "url") || strings.EqualFold(key, "endpoint")) && credentialBearingURL(text)
}

func redactConfig(raw []byte, all bool) (datatypes.JSON, []string) {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return datatypes.JSON(`{}`), []string{}
	}
	configured := []string{}
	var walk func(any, string)
	walk = func(current any, prefix string) {
		switch obj := current.(type) {
		case map[string]any:
			for key, val := range obj {
				path := prefix + key
				if all || secretField(key) || secretURLField(key, val) {
					if val != nil && val != "" {
						configured = append(configured, path)
					}
					delete(obj, key)
				} else {
					walk(val, path+".")
				}
			}
		case []any:
			for index, child := range obj {
				walk(child, prefix+strconv.Itoa(index)+".")
			}
		}
	}
	walk(value, "")
	sort.Strings(configured)
	encoded, _ := json.Marshal(value)
	return encoded, configured
}

func publicSetting(item *model.Setting) settingView {
	view := settingView{Setting: *item}
	view.Value, view.ConfiguredFields = redactConfig(item.Value, secretField(item.Name))
	return view
}
func publicStorage(item *model.StorageConfig) storageConfigView {
	view := storageConfigView{StorageConfig: *item}
	view.Config, view.ConfiguredFields = redactConfig(item.Config, false)
	// Only documented noncredential storage fields are readable.
	var safe map[string]any
	_ = json.Unmarshal(view.Config, &safe)
	for field := range safe {
		switch field {
		case "endpoint", "bucket", "region", "useTLS", "basePath":
		default:
			delete(safe, field)
		}
	}
	if endpoint, ok := safe["endpoint"].(string); ok && credentialBearingURL(endpoint) {
		delete(safe, "endpoint")
		view.ConfiguredFields = append(view.ConfiguredFields, "endpoint")
	}
	view.Config, _ = json.Marshal(safe)
	return view
}

func secretPlaceholder(value string) bool {
	s := strings.ToLower(strings.TrimSpace(value))
	return s == "[redacted]" || s == "redacted" || s == "<redacted>" || s == "configured" || s == "[configured]" || s == "********" || s == "******" || s == "••••••••"
}

// Updates merge into existing objects. Missing or empty secret fields keep the
// existing value; removal requires explicit deletion of the configuration.
// The returned bool indicates whether the request supplied a secret field.
func containsConfigSecrets(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if secretField(key) || secretURLField(key, child) || containsConfigSecrets(child) {
				return true
			}
		}
	case []any:
		for _, child := range item {
			if containsConfigSecrets(child) {
				return true
			}
		}
	}
	return false
}

func mergeConfigSecrets(previous, input []byte, all bool) ([]byte, bool, error) {
	old, next := map[string]any{}, map[string]any{}
	if len(previous) != 0 && (json.Unmarshal(previous, &old) != nil || old == nil) {
		return nil, false, fmt.Errorf("stored configuration is invalid")
	}
	if len(input) == 0 {
		input = []byte(`{}`)
	}
	if json.Unmarshal(input, &next) != nil || next == nil {
		return nil, false, fmt.Errorf("configuration must be a JSON object")
	}
	secretInput := false
	var merge func(any, any) (any, error)
	merge = func(previous, input any) (any, error) {
		switch next := input.(type) {
		case map[string]any:
			prior, ok := previous.(map[string]any)
			if !ok {
				if containsConfigSecrets(previous) {
					return nil, fmt.Errorf("cannot replace a secret-bearing configuration container")
				}
				prior = map[string]any{}
			}
			for key, val := range next {
				if secretURLField(key, val) {
					return nil, fmt.Errorf("configuration URLs must not contain credentials, query parameters or fragments")
				}
				if all || secretField(key) || secretURLField(key, prior[key]) {
					secretInput = true
					if val == nil || val == "" {
						continue
					}
					text, ok := val.(string)
					if !ok || secretPlaceholder(text) {
						return nil, fmt.Errorf("secret fields require a new value or must be omitted to preserve the existing value")
					}
					prior[key] = text
				} else {
					merged, err := merge(prior[key], val)
					if err != nil {
						return nil, err
					}
					prior[key] = merged
				}
			}
			return prior, nil
		case []any:
			prior, ok := previous.([]any)
			if !ok && containsConfigSecrets(previous) {
				return nil, fmt.Errorf("cannot replace a secret-bearing configuration container")
			}
			for index := len(next); index < len(prior); index++ {
				if containsConfigSecrets(prior[index]) {
					return nil, fmt.Errorf("cannot remove secret-bearing configuration entries")
				}
			}
			merged := make([]any, len(next))
			for index, item := range next {
				var oldItem any
				if index < len(prior) {
					oldItem = prior[index]
				}
				value, err := merge(oldItem, item)
				if err != nil {
					return nil, err
				}
				merged[index] = value
			}
			return merged, nil
		default:
			if containsConfigSecrets(previous) {
				return nil, fmt.Errorf("cannot replace a secret-bearing configuration container")
			}
			return input, nil
		}
	}
	merged, err := merge(old, next)
	if err != nil {
		return nil, secretInput, err
	}
	encoded, err := json.Marshal(merged)
	return encoded, secretInput, err
}

func credentialBearingURL(value string) bool {
	candidate := value
	if !strings.Contains(candidate, "://") {
		candidate = "//" + candidate
	}
	parsed, err := url.Parse(candidate)
	return err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != ""
}
func validateStorageEndpoint(raw []byte) error {
	var config map[string]any
	if json.Unmarshal(raw, &config) != nil {
		return fmt.Errorf("invalid storage configuration")
	}
	for key, value := range config {
		if strings.EqualFold(key, "endpoint") {
			endpoint, ok := value.(string)
			if !ok || credentialBearingURL(endpoint) {
				return fmt.Errorf("storage endpoint must not contain credentials, query parameters or fragments")
			}
		}
	}
	return nil
}
