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

package consts

const SettingKeyPrefix = "setting:"

// Setting names in the t_setting table (workspace-scoped).
const (
	SettingNameGeneral                     = "GENERAL"
	SettingNameExternalURL                 = "EXTERNAL_URL"
	SettingNameAgentSecretKey              = "AGENT_SECRET_KEY"
	SettingNameAgentHeartbeatExpireSeconds = "AGENT_HEARTBEAT_EXPIRE_SECONDS"
	SettingNameTaskHistoryExpireSeconds    = "TASK_HISTORY_EXPIRE_SECONDS"
)

// JSON value keys used inside t_setting.value payloads.
const (
	SettingKeyExternalURL = "external_url"
	SettingKeySecretKey   = "secret_key"
	SettingKeySalt        = "salt"
)
