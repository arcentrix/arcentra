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

package id_test

import (
	"fmt"
	"testing"

	"github.com/arcentrix/arcentra/pkg/id"
)

func TestGetXid(t *testing.T) {
	tests := []struct {
		name string
	}{
		{
			name: "generate_xid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := id.GetXid()

			// XID 应该是 20 个字符的字符串
			if len(got) != 20 {
				t.Errorf("GetXid() length = %d, want 20", len(got))
			}

			// XID 不应该为空
			if got == "" {
				t.Errorf("GetXid() returned empty string")
			}

			// 验证生成的 XID 是唯一的（生成两个应该不同）
			got2 := id.GetXid()
			if got == got2 {
				t.Errorf("GetXid() generated duplicate IDs: %s", got)
			}

			fmt.Println("Generated XID 1:", got)

			t.Logf("Generated XID: %s", got)
		})
	}
}
