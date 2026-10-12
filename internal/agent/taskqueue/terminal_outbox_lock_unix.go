//go:build unix

package taskqueue

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockTerminalOutbox(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
func validateTerminalOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("terminal report outbox must be owned by the current user")
	}
	if info.Mode().IsRegular() && stat.Nlink != 1 {
		return errors.New("terminal report outbox must not contain hard-linked files")
	}
	return nil
}
