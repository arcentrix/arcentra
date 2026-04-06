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

// SetIfNotNil sets the value in the map if the pointer is not nil
// This is useful for building update maps where zero values should be included
// Example:
//
//	updates := make(map[string]any)
//	SetIfNotNil(updates, "is_enabled", req.IsEnabled)  // Will set even if IsEnabled is 0
func SetIfNotNil[T any](m map[string]any, key string, ptr *T) {
	if ptr != nil {
		m[key] = *ptr
	}
}
