//go:build !unix

package executor

import (
	"errors"
	"os"
)

func lockBuildLogSpool(_ *os.File) error {
	return errors.New("durable build log spooling requires a platform with advisory file locking")
}

func validateLogSpoolOwner(_ os.FileInfo) error {
	return errors.New("durable build log spooling requires a platform with file ownership validation")
}
