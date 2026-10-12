//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris)

package identitystore

import (
	"errors"
	"os"
)

func supportedPlatform() error {
	return errors.New("agent identity persistence requires supported Unix ownership and locking semantics")
}
func privateOpenFlags() int                      { return 0 }
func lockExclusive(*os.File) error               { return supportedPlatform() }
func validatePrivateDirectory(os.FileInfo) error { return supportedPlatform() }
func validateTrustedAncestor(os.FileInfo) error  { return supportedPlatform() }
func validatePrivateFile(os.FileInfo) error      { return supportedPlatform() }
