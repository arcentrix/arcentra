//go:build !unix

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package scm

import "os/exec"

func configureDefinitionCancellation(_ *exec.Cmd) {}
