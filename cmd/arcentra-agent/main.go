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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/arcentrix/arcentra/internal/agent/bootstrap"
	"github.com/arcentrix/arcentra/internal/agent/terminalops"
)

func main() {
	// 离线隔离审查必须在加载配置、注册身份或建立任何网络客户端之前分派
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	handled, exitCode := terminalops.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if handled {
		os.Exit(exitCode)
	}

	// Parse command line flags
	configFile := flag.String("conf", "conf.d/agent.toml", "configuration file path, e.g. -conf ./conf.d/agent.toml")
	flag.Parse()
	if flag.NArg() != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "error: unexpected Agent arguments; use terminal-quarantine --help for offline review")
		os.Exit(2)
	}

	// Bootstrap initialize application
	app, cleanup, _, err := bootstrap.Bootstrap(*configFile, initAgent)
	if err != nil {
		_, err = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if err != nil {
			return
		}
		os.Exit(1)
	}

	// Start application and wait for exit signal
	bootstrap.Run(app, cleanup)
}
