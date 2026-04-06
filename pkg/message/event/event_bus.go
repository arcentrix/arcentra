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

package event

import "fmt"

type Bus struct {
	handlers map[string][]Handler
}

func NewEventBus() *Bus {
	return &Bus{
		handlers: make(map[string][]Handler),
	}
}

func (eb *Bus) RegisterHandler(eventName string, handler Handler) {
	if _, ok := eb.handlers[eventName]; !ok {
		eb.handlers[eventName] = make([]Handler, 0)
	}
	eb.handlers[eventName] = append(eb.handlers[eventName], handler)
}

func (eb *Bus) Publish(event Event) {
	eventName := event.EventName()
	if handlers, ok := eb.handlers[eventName]; ok {
		fmt.Println("event:", eb)
		for _, handler := range handlers {
			handler.Handle(event)
		}
	}
}

func (eb *Bus) Consume(event Event) {
	eb.Publish(event)
}
