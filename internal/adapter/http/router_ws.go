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

package http

import (
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) wsRoutes(r fiber.Router, auth fiber.Handler) {
	r.Get("/ws", auth, rt.handleWS)
}

func (rt *Router) handleWS(c *fiber.Ctx) error {
	// WebSocket log streaming is handled by the ManageStepRun use case.
	// The actual WebSocket upgrade and message handling will be wired
	// through the ws.Handle infrastructure once the execution use case
	// exposes the necessary streaming interface.
	return rt.ManageStepRun.HandleWebSocket(c)
}
