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

package safe

import "testing"

func TestDo(t *testing.T) {
	panicFunc := func() {
		panic("test panic")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Do did not recover from panic: %v", r)
		}
	}()

	do(panicFunc)
}

func TestGo(t *testing.T) {
	done := make(chan bool)
	panicFunc := func() {
		defer func() {
			done <- true
		}()
		panic("test panic in goroutine")
	}

	Go(panicFunc)
	<-done
}
