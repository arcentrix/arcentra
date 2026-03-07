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

package ringbuffer

import (
	"testing"
)

func TestYieldingWaitStrategy_Wait(t *testing.T) {
	y := &YieldingWaitStrategy{}
	// Should not block indefinitely; just yield
	for i := 0; i < 3; i++ {
		y.Wait()
	}
}

func TestSleepWaitStrategy_Wait(t *testing.T) {
	s := &SleepWaitStrategy{}
	s.Wait()
	s.Wait()
	// No panic (zero duration sleeps briefly)
}
