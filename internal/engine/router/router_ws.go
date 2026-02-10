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

package router

import (
	"github.com/arcentrix/arcentra/internal/engine/service"
	"github.com/arcentrix/arcentra/pkg/ws"
	"github.com/gofiber/fiber/v2"
)

// wsRouter WebSocket路由
func (rt *Router) wsRouter(r fiber.Router, auth fiber.Handler) {
	// WebSocket
	wsHub := ws.NewHub()
	kafkaCfg := rt.AppConf.MessageQueue.Kafka
	wsHandle := service.NewWSHandle(wsHub, rt.Services.LogAggregator, rt.Services.StepRunRepo, kafkaCfg)
	r.Get("/ws", auth, ws.Handle(wsHub, wsHandle))
}
