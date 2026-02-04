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

package main

import (
	"flag"

	"github.com/arcentrix/arcade/internal/engine/bootstrap"
	_ "github.com/arcentrix/arcade/pkg/plugins/git"
	_ "github.com/arcentrix/arcade/pkg/plugins/svn"
)

var (
	configFile string
)

func init() {
	flag.StringVar(&configFile, "conf", "conf.d/config.toml", "config file path, e.g. -conf ./conf.d")
}

func main() {
	flag.Parse()

	// Bootstrap 初始化应用
	app, cleanup, _, err := bootstrap.Bootstrap(configFile, initApp)
	if err != nil {
		panic(err)
	}

	// 启动应用并等待退出信号
	bootstrap.Run(app, cleanup)
}
