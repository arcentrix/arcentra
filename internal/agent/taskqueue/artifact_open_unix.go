//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris

package taskqueue

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// openArtifactRegularFile 使用固定目录句柄逐级拒绝链接，防止 FIFO 在类型检查前阻塞
func openArtifactRegularFile(root *os.Root, name string, expected os.FileInfo) (*os.File, error) {
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid artifact path")
		}
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	for _, part := range parts[:len(parts)-1] {
		fd, err := unix.Openat(int(directory.Fd()), part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		if err != nil {
			return nil, err
		}
		next := os.NewFile(uintptr(fd), part)
		_ = directory.Close()
		directory = next
	}
	fd, err := unix.Openat(int(directory.Fd()), parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || expected == nil || !os.SameFile(expected, actual) {
		_ = file.Close()
		return nil, fmt.Errorf("artifact changed while opening")
	}
	return file, nil
}
