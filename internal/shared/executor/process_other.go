//go:build !unix

package executor

import "os/exec"

func configureProcessCancellation(_ *exec.Cmd) {}
