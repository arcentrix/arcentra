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

package middleware

import (
	"strings"
	"time"

	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/arcentrix/arcentra/pkg/log"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

type writerFunc func(p []byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) {
	return w(p)
}

func AccessLogMiddleware(httpConfig *http.Http) fiber.Handler {
	// exclude api path
	// tips: 这里的路径是不需要记录日志的路径，url为端口后的全部路径
	excludedPaths := []string{
		"/health",
		"/metrics",
		"/debug/pprof/*",
	}

	if httpConfig != nil && !httpConfig.AccessLog {
		return func(c *fiber.Ctx) error {
			return c.Next()
		}
	}

	return logger.New(logger.Config{
		TimeFormat: time.RFC3339Nano,
		TimeZone:   "Local",
		Format:     "ip:[${ip}] ips:[${ips}] method:[${method}] path:[${path}] latency:[${latency}] status:[${status}] resBody:[${resBody}] queryParams:[${queryParams}] body:[${body}] error:[${error}] header:[${header:}] reqHeader:[${reqHeader:}] respHeader:[${respHeader:}] query:[${query:}] form:[${form:}] cookie:[${cookie:}] locals:[${locals:}] ua:[${ua}] ",
		Next: func(c *fiber.Ctx) bool {
			path := c.Path()
			for _, rule := range excludedPaths {
				if before, ok := strings.CutSuffix(rule, "/*"); ok {
					prefix := before
					if strings.HasPrefix(path, prefix) {
						return true
					}
				} else if path == rule {
					return true
				}
			}
			return false
		},
		Output: writerFunc(func(p []byte) (int, error) {
			log.Debug(strings.TrimSpace(string(p)))
			return len(p), nil
		}),
	})
}
