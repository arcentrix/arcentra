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

package event

import (
	"fmt"
	"testing"
)

type TestEvent struct {
	Name   string
	Detail Detail
}

type Detail struct {
	Type string
	Data string
}

func (e TestEvent) EventName() string {
	return e.Name
}

func (e TestEvent) EventType() string {
	return e.Detail.Type
}

type TestHandler struct{}

func (h TestHandler) Handle(event Event) {
	if customEvent, ok := event.(TestEvent); ok {
		fmt.Printf("Handled event: %s with detail: %s\n", customEvent.Name, customEvent.Detail.Type)
	}
}

func TestNewEventBus(t *testing.T) {
	bus := NewEventBus()
	if bus == nil {
		t.Error("NewEventBus failed")
	}

	bus.RegisterHandler("test", TestHandler{})

	event := TestEvent{
		Name: "test",
		Detail: Detail{
			Type: "test",
			Data: "test",
		},
	}

	bus.Publish(event)
}
