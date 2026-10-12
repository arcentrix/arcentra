//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

package identitystore

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func supportedPlatform() error { return nil }

func privateOpenFlags() int { return unix.O_NOFOLLOW | unix.O_NONBLOCK }

func lockExclusive(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ErrLocked
		}
		return errIO
	}
	return nil
}

func validatePrivateDirectory(info os.FileInfo) error {
	if info == nil || !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&(os.ModeSymlink|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errUnsafe
	}
	return nil
}

func validateTrustedAncestor(info os.FileInfo) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
		return errUnsafe
	}
	if info.Mode().Perm()&0o022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
		return errUnsafe
	}
	return nil
}

func validatePrivateFile(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return errUnsafe
	}
	return nil
}
