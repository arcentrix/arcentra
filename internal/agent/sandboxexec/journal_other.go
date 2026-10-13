//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris)

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"fmt"
	"os"
)

func journalUnsupported() error {
	return fmt.Errorf("durable sandbox sessions require Unix ownership and exclusive locking")
}
func journalOpenFlags() int                                     { return 0 }
func journalLock(*os.File) error                                { return journalUnsupported() }
func privateJournalNode(os.FileInfo, bool) error                { return journalUnsupported() }
func trustedJournalAncestor(os.FileInfo) error                  { return journalUnsupported() }
func workspaceFileIdentity(os.FileInfo) (uint64, uint64, error) { return 0, 0, journalUnsupported() }
