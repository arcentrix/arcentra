//go:build unix

package executor

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func lockBuildLogSpool(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func validateLogSpoolOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("build log spool must be owned by the current process user")
	}
	if info.Mode().IsRegular() && stat.Nlink != 1 {
		return errors.New("build log spool must not contain hard-linked files")
	}
	return nil
}
