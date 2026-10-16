package router

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/pkg/configsecret"
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

func secretField(name string) bool { return configsecret.IsSecretField(name) }
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

func mergeConfigSecrets(previous, input []byte, all bool) ([]byte, bool, error) {
	return configsecret.Merge(previous, input, all)
}
func credentialBearingURL(value string) bool   { return configsecret.CredentialBearingURL(value) }
func validateStorageEndpoint(raw []byte) error { return configsecret.ValidateStorageEndpoint(raw) }
