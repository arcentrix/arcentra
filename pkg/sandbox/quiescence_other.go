//go:build !linux

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"fmt"
)

func pinInitProcess(context.Context, uint32) (pinnedInit, error) {
	return nil, fmt.Errorf("quiescence proof requires Linux pidfd support")
}
