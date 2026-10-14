// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type initPIDFD struct {
	fd  int
	pid uint32
}

func pinInitProcess(ctx context.Context, pid uint32) (pinnedInit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.PidfdOpen(int(pid), 0)
	if err != nil {
		// No fallback to /proc names, numeric PIDs or starttime heuristics.
		return nil, fmt.Errorf("pidfd_open is required: %w", err)
	}
	pin := &initPIDFD{fd: fd, pid: pid}
	if err = pin.alive(ctx); err != nil {
		_ = pin.close()
		return nil, err
	}
	return pin, nil
}

func (p *initPIDFD) alive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.fd < 0 {
		return fmt.Errorf("init pidfd is closed")
	}
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	if err != nil {
		return fmt.Errorf("inspect init pidfd: %w", err)
	}
	if n != 0 || fds[0].Revents != 0 {
		return fmt.Errorf("pinned init exited or pidfd is invalid")
	}
	return ctx.Err()
}

func (p *initPIDFD) close() error {
	if p.fd < 0 {
		return nil
	}
	fd := p.fd
	p.fd = -1
	return unix.Close(fd)
}

func (p *initPIDFD) childless(ctx context.Context) error {
	if err := p.alive(ctx); err != nil {
		return err
	}
	if err := readInitChildren(ctx, fmt.Sprintf("/proc/%d/task", p.pid)); err != nil {
		return fmt.Errorf("verify pinned init descendants: %w", err)
	}
	return p.alive(ctx)
}

// Read every thread's child list, rather than cgroup.procs (which omits
// zombies) or just the main thread's children. Visibility is mandatory.
func readInitChildren(ctx context.Context, directory string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	threads, err := dir.ReadDir(129)
	if err != nil && err != io.EOF {
		return err
	}
	if len(threads) == 0 || len(threads) > 128 {
		return fmt.Errorf("init thread enumeration is empty or exceeds bound")
	}
	for _, thread := range threads {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := strconv.ParseUint(thread.Name(), 10, 32); err != nil || !thread.IsDir() || thread.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected init thread identity")
		}
		file, err := root.Open(thread.Name() + "/children")
		if err != nil {
			return err
		}
		children, readErr := io.ReadAll(io.LimitReader(file, 32769))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(children) > 32768 {
			return fmt.Errorf("init child list exceeds bound")
		}
		if strings.TrimSpace(string(children)) != "" {
			return fmt.Errorf("pinned init retains child processes, including possible zombies")
		}
	}
	return ctx.Err()
}
