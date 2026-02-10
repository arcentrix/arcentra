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
	"flag"
	"fmt"
	"os"

	"github.com/arcentrix/arcentra/internal/agent/bootstrap"
)

func main() {
	// Parse command line flags
	configFile := flag.String("conf", "conf.d/agent.toml", "configuration file path, e.g. -conf ./conf.d/agent.toml")
	flag.Parse()

	// Bootstrap initialize application
	app, cleanup, _, err := bootstrap.Bootstrap(*configFile, initAgent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Start application and wait for exit signal
	bootstrap.Run(app, cleanup)
}
