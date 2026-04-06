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

package id

import (
	"fmt"
	"testing"
)

func TestGenUUID(t *testing.T) {
	uuid := GetUUID()
	if len(uuid) != 36 {
		t.Errorf("uuid length is not 36")
	}
	fmt.Printf("uuid: %s", uuid)
}

func TestGetUUIDWithoutDashes(t *testing.T) {
	uuid := GetUUIDWithoutDashes()
	if len(uuid) != 32 {
		t.Error("uuid length is not 32")
	}
	fmt.Printf("uuid: %s", uuid)
}
