//go:build !unix

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package taskqueue

import "os/exec"

func configureSourceGitCancellation(_ *exec.Cmd) {}
