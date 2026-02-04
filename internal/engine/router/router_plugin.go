// Copyright 2025 Arcade Team
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
	"github.com/arcentrix/arcade/pkg/http"
	"github.com/arcentrix/arcade/pkg/http/middleware"
	"github.com/gofiber/fiber/v2"
)

func (rt *Router) pluginRouter(r fiber.Router, auth fiber.Handler) {
	pluginGroup := r.Group("/plugins", auth)
	{
		// GET /plugins - 获取插件列表（支持按 pluginId 过滤）
		pluginGroup.Get("", rt.listPlugins)
		// GET /plugins/:pluginId - 获取指定插件的所有版本
		pluginGroup.Get("/:pluginId", rt.getPluginVersions)
		// GET /plugins/:pluginId/versions/:version - 获取指定插件指定版本的详情
		pluginGroup.Get("/:pluginId/versions/:version", rt.getPlugin)
	}
}

// listPlugins GET /plugins - 获取插件列表
// 查询参数:
//   - pluginId: 可选，如果提供则只返回该插件的所有版本
func (rt *Router) listPlugins(c *fiber.Ctx) error {
	pluginService := rt.Services.Plugin

	// 获取查询参数
	pluginId := c.Query("pluginId", "")

	plugins, err := pluginService.ListPlugins(pluginId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	result := make(map[string]any)
	result["plugins"] = plugins
	result["count"] = len(plugins)

	c.Locals(middleware.DETAIL, result)
	return nil
}

// getPluginVersions GET /plugins/:pluginId - 获取指定插件的所有版本
func (rt *Router) getPluginVersions(c *fiber.Ctx) error {
	pluginId := c.Params("pluginId")
	if pluginId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "plugin id is required", c.Path())
	}

	pluginService := rt.Services.Plugin
	plugins, err := pluginService.ListPlugins(pluginId)
	if err != nil {
		return http.WithRepErrMsg(c, http.Failed.Code, http.Failed.Msg, c.Path())
	}

	result := make(map[string]any)
	result["pluginId"] = pluginId
	result["versions"] = plugins
	result["count"] = len(plugins)

	c.Locals(middleware.DETAIL, result)
	return nil
}

// getPlugin GET /plugins/:pluginId/versions/:version - 获取指定插件指定版本的详情
func (rt *Router) getPlugin(c *fiber.Ctx) error {
	pluginId := c.Params("pluginId")
	version := c.Params("version")

	if pluginId == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "plugin id is required", c.Path())
	}
	if version == "" {
		return http.WithRepErrMsg(c, http.BadRequest.Code, "version is required", c.Path())
	}

	pluginService := rt.Services.Plugin
	plugin, err := pluginService.GetPlugin(pluginId, version)
	if err != nil {
		return http.WithRepErrMsg(c, http.NotFound.Code, "plugin not found", c.Path())
	}

	c.Locals(middleware.DETAIL, plugin)
	return nil
}
