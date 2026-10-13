//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func journalOpenFlags() int { return unix.O_NOFOLLOW | unix.O_NONBLOCK }

func journalLock(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("sandbox journal requires exclusive ownership: %w", err)
	}
	return nil
}

func privateJournalNode(info os.FileInfo, directory bool) error {
	if info == nil || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("sandbox journal has unsafe file type")
	}
	expected := os.FileMode(0o600)
	if directory {
		expected = 0o700
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok ||
		stat.Uid != uint32(os.Geteuid()) ||
		info.Mode().Perm() != expected ||
		info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		!directory && stat.Nlink != 1 {
		return fmt.Errorf("sandbox journal has unsafe ownership, permissions, or links")
	}
	return nil
}

func trustedJournalAncestor(info os.FileInfo) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("sandbox root has unsafe ancestor")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok ||
		stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) ||
		info.Mode().Perm()&0o022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return fmt.Errorf("sandbox root has unsafe ancestor ownership or permissions")
	}
	return nil
}

func workspaceFileIdentity(info os.FileInfo) (uint64, uint64, error) {
	if err := privateJournalNode(info, true); err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("sandbox workspace identity is unavailable")
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}
