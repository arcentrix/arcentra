//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris)

package taskqueue

import (
	"fmt"
	"os"
)

func openArtifactRegularFile(*os.Root, string, os.FileInfo) (*os.File, error) {
	return nil, fmt.Errorf("artifact upload requires supported nonblocking no-follow file opening")
}
