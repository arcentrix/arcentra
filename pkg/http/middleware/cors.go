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

package middleware

import (
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

var (
	allowMethods  = "GET, POST, PUT, DELETE, OPTIONS"
	allowHeaders  = "Origin, X-Requested-With, Content-Type, Accept, Authorization"
	exposeHeaders = "Content-Length, Access-Control-Allow-Origin, Access-Control-Allow-Headers, Cache-Control, Content-Language, Content-Type"
)

// CorsMiddleware 跨域中间件
func CorsMiddleware() fiber.Handler {
	// NOTE:
	// - When AllowCredentials is true, Fiber CORS forbids AllowOrigins="*".
	// - For local dev (Vite/React), requests typically come from http://localhost:5173.
	//
	// Configure allowed origins via env:
	//   ARCENTRA_CORS_ALLOW_ORIGINS="http://localhost:5173,http://127.0.0.1:5173"
	// If not set, we default to localhost dev origins.
	allowed := strings.TrimSpace(os.Getenv("ARCENTRA_CORS_ALLOW_ORIGINS"))
	allowedSet := map[string]struct{}{}
	if allowed == "" {
		allowed = "http://localhost:5173,http://127.0.0.1:5173"
	}
	for o := range strings.SplitSeq(allowed, ",") {
		o = strings.ToLower(strings.TrimSpace(o))
		if o == "" {
			continue
		}
		allowedSet[o] = struct{}{}
	}

	return cors.New(cors.Config{
		AllowOriginsFunc: func(origin string) bool {
			origin = strings.ToLower(strings.TrimSpace(origin))
			_, ok := allowedSet[origin]
			return ok
		},
		AllowMethods:     allowMethods,
		AllowHeaders:     allowHeaders,
		ExposeHeaders:    exposeHeaders,
		AllowCredentials: true,
	})
}
