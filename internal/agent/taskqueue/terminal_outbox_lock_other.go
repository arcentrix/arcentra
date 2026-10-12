//go:build !unix

package taskqueue

import (
	"errors"
	"os"
)

func lockTerminalOutbox(_ *os.File) error {
	return errors.New("terminal report outbox requires advisory file locking")
}
func validateTerminalOwner(_ os.FileInfo) error {
	return errors.New("terminal report outbox requires file ownership validation")
}
