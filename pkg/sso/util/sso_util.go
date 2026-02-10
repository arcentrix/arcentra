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

package util

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
)

var StateStore = &sync.Map{}

// StateData contains data stored in state
type StateData struct {
	ProviderName string `json:"providerName"`
	RedirectURI  string `json:"redirectURI,omitempty"`
}

// GenState generate a random state string
func GenState() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(b)
}

// StoreState stores state data
func StoreState(state string, providerName string) {
	data := StateData{
		ProviderName: providerName,
	}
	StateStore.Store(state, data)
}

// LoadAndDeleteState load and delete the state from the state store
func LoadAndDeleteState(state string) (StateData, bool) {
	value, ok := StateStore.LoadAndDelete(state)
	if ok {
		if data, ok := value.(StateData); ok {
			return data, true
		}
		// Backward compatibility: if stored as string (old format), treat as providerName only
		if str, ok := value.(string); ok {
			return StateData{ProviderName: str}, true
		}
		return StateData{}, false
	}
	return StateData{}, false
}

// CheckState checks if a state exists in the store without deleting it (for debugging)
func CheckState(state string) (StateData, bool) {
	value, ok := StateStore.Load(state)
	if ok {
		if data, ok := value.(StateData); ok {
			return data, true
		}
		// Backward compatibility
		if str, ok := value.(string); ok {
			return StateData{ProviderName: str}, true
		}
		return StateData{}, false
	}
	return StateData{}, false
}
