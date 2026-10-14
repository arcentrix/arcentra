// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bytedance/sonic"
	"github.com/containerd/containerd/cio"
)

func validateFIFODir(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == "/" || strings.ContainsAny(dir, "\x00\r\n") {
		return fmt.Errorf("FIFO directory must be a clean absolute private path")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return fmt.Errorf("FIFO directory must exist without symlink components")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("FIFO directory must be a pre-existing private directory with mode 0700")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("FIFO directory must belong to the Agent user")
	}
	for parent := filepath.Dir(dir); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("FIFO directory ancestors must be real trusted directories")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (owner.Uid != 0 && int(owner.Uid) != os.Geteuid()) || (info.Mode().Perm()&0022 != 0 && !(owner.Uid == 0 && info.Mode()&os.ModeSticky != 0)) {
			return fmt.Errorf("FIFO directory ancestors must not allow untrusted replacement")
		}
		if parent == string(filepath.Separator) {
			break
		}
	}
	return nil
}

// The sibling marker is committed before any FIFO directory exists and removed
// only after that directory is gone. A crash at either boundary remains retryable.
// No marker is adopted or repaired, and no unrelated directory is enumerated.
const maxRuntimeIOSets = 4096
const maxRuntimeIOMarkerBytes = 4096

type runtimeIOMarker struct {
	Version int         `json:"version"`
	Ref     ResourceRef `json:"ref"`
}

type runtimeIONode struct {
	path string
	info os.FileInfo
}

func runtimeIOIdentity(ref ResourceRef) (string, []byte, error) {
	if err := validateIdentity(ref.ID, ref.Owner); err != nil {
		return "", nil, err
	}
	if ref.Namespace == "" || len(ref.Namespace) > 253 || strings.ContainsAny(ref.Namespace, "\x00\r\n/") || ref.Snapshotter == "" || len(ref.Snapshotter) > 128 || strings.ContainsAny(ref.Snapshotter, "\x00\r\n/") {
		return "", nil, fmt.Errorf("FIFO resource identity is incomplete or invalid")
	}
	data, err := sonic.Marshal(runtimeIOMarker{Version: 1, Ref: ref})
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), data, nil
}

func openRuntimeIORoot(dir string) (*os.Root, error) {
	if err := validateFIFODir(dir); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

func privateRuntimeIONode(info os.FileInfo, kind os.FileMode, mode os.FileMode) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Type() != kind || info.Mode().Perm() != mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return fmt.Errorf("FIFO node must have the expected type, private mode and Agent owner")
	}
	if kind != os.ModeDir && stat.Nlink != 1 {
		return fmt.Errorf("FIFO node must not have additional hard links")
	}
	return nil
}

func syncRuntimeIODir(root *os.Root, path string) error {
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

// The caller retains this open marker while reading or modifying its directory.
// A nonblocking advisory lock serializes cooperating Agent instances without an
// unbounded filesystem wait. Rechecking its inode rejects an unlinked old marker.
func openRuntimeIOMarker(root *os.Root, name string, expected []byte, create bool) (*os.File, error) {
	marker := name + ".owner"
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	created := false
	file, err := root.OpenFile(marker, flags, 0)
	if os.IsNotExist(err) && create {
		if _, dirErr := root.Lstat(name); !os.IsNotExist(dirErr) {
			return nil, fmt.Errorf("refuse to adopt FIFO directory without owner marker")
		}
		file, err = root.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		created = err == nil
	}
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) { _ = file.Close(); return nil, err }
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(fmt.Errorf("lock FIFO ownership marker: %w", err))
	}
	info, err := file.Stat()
	if err != nil {
		return fail(err)
	}
	if err = privateRuntimeIONode(info, 0, 0600); err != nil {
		return fail(err)
	}
	current, err := root.Lstat(marker)
	if err != nil || !os.SameFile(info, current) {
		return fail(fmt.Errorf("FIFO ownership marker was replaced"))
	}
	if created {
		_, err = file.Write(expected)
	} else {
		var data []byte
		data, err = io.ReadAll(io.LimitReader(file, maxRuntimeIOMarkerBytes+1))
		if err == nil && !bytes.Equal(data, expected) {
			err = fmt.Errorf("FIFO ownership marker does not match exact resource identity")
		}
	}
	// An existing exact marker can be an earlier attempt whose final fsync
	// failed. Re-establish durability before authorizing new runtime IO.
	if err == nil && create {
		err = file.Sync()
		if err == nil {
			err = syncRuntimeIODir(root, ".")
		}
	}
	if err != nil {
		return fail(err)
	}
	return file, nil
}

func readRuntimeIODir(root *os.Root, path string, limit int) ([]os.DirEntry, os.FileInfo, error) {
	// os.Root can resolve symlinks internally before its final openat, so the
	// O_NOFOLLOW flag alone is not sufficient to reject a symlink directory.
	before, err := root.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if err = privateRuntimeIONode(before, os.ModeDir, 0700); err != nil {
		return nil, nil, err
	}
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if err = privateRuntimeIONode(info, os.ModeDir, 0700); err != nil {
		return nil, nil, err
	}
	after, err := root.Lstat(path)
	if err != nil || !os.SameFile(before, info) || !os.SameFile(info, after) {
		return nil, nil, fmt.Errorf("FIFO directory changed during inspection")
	}
	entries, err := file.ReadDir(limit + 1)
	if err == io.EOF {
		err = nil
	}
	if len(entries) > limit {
		err = fmt.Errorf("FIFO directory exceeds bounded entry count")
	}
	return entries, info, err
}

// Preflight the entire tree before the first unlink. Only our fixed two-level
// shape, private directories and named pipes are accepted, including partial IO
// allocation. Regular files, extra names and symlinks are preserved as evidence.
func inspectRuntimeIO(ctx context.Context, root *os.Root, name string) ([]runtimeIONode, int, error) {
	entries, info, err := readRuntimeIODir(root, name, maxRuntimeIOSets)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var leaves, directories []runtimeIONode
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		child := entry.Name()
		if len(child) != 35 || !strings.HasPrefix(child, "io-") {
			return nil, 0, fmt.Errorf("unexpected FIFO child directory")
		}
		if _, err := hex.DecodeString(child[3:]); err != nil {
			return nil, 0, fmt.Errorf("malformed FIFO child directory")
		}
		path := filepath.Join(name, child)
		streams, childInfo, err := readRuntimeIODir(root, path, 3)
		if err != nil {
			return nil, 0, err
		}
		for _, stream := range streams {
			if stream.Name() != "stdin" && stream.Name() != "stdout" && stream.Name() != "stderr" {
				return nil, 0, fmt.Errorf("unexpected FIFO stream name")
			}
			streamPath := filepath.Join(path, stream.Name())
			streamInfo, err := root.Lstat(streamPath)
			if err != nil {
				return nil, 0, err
			}
			if err = privateRuntimeIONode(streamInfo, os.ModeNamedPipe, 0700); err != nil {
				return nil, 0, err
			}
			leaves = append(leaves, runtimeIONode{streamPath, streamInfo})
		}
		directories = append(directories, runtimeIONode{path, childInfo})
	}
	nodes := append(leaves, directories...)
	return append(nodes, runtimeIONode{name, info}), len(entries), nil
}

func newRuntimeIO(ctx context.Context, dir string, ref ResourceRef, stdin io.Reader, stdout, stderr io.Writer) cio.Creator {
	if dir == "" {
		return cio.NewCreator(cio.WithStreams(stdin, stdout, stderr))
	}
	return func(_ string) (cio.IO, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, markerData, err := runtimeIOIdentity(ref)
		if err != nil {
			return nil, err
		}
		root, err := openRuntimeIORoot(dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		marker, err := openRuntimeIOMarker(root, name, markerData, true)
		if err != nil {
			return nil, err
		}
		defer marker.Close()
		_, count, err := inspectRuntimeIO(ctx, root, name)
		if err != nil {
			return nil, err
		}
		if count >= maxRuntimeIOSets {
			return nil, fmt.Errorf("FIFO resource exceeds bounded IO set count")
		}
		if err = root.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		child := filepath.Join(name, "io-"+hex.EncodeToString(nonce[:]))
		if err = root.Mkdir(child, 0700); err != nil {
			return nil, err
		}
		if err = syncRuntimeIODir(root, "."); err == nil {
			err = syncRuntimeIODir(root, name)
		}
		if err != nil {
			return nil, err
		}
		config := cio.Config{}
		if stdin != nil {
			config.Stdin = filepath.Join(dir, child, "stdin")
		}
		if stdout != nil {
			config.Stdout = filepath.Join(dir, child, "stdout")
		}
		if stderr != nil {
			config.Stderr = filepath.Join(dir, child, "stderr")
		}
		// Close releases handles but must not remove paths: a failed task/exec RPC
		// can still have created a live runtime process. Exact-owned removal below
		// is the only operation authorized to unlink these durable paths.
		stream, err := cio.NewAttach(cio.WithStreams(stdin, stdout, stderr))(cio.NewFIFOSet(config, nil))
		if err != nil {
			return nil, err
		}
		if err = syncRuntimeIODir(root, child); err != nil {
			stream.Cancel()
			stream.Wait()
			_ = stream.Close()
			return nil, err
		}
		return stream, nil
	}
}

// Called only after runtime ownership checks and confirmed task absence. The
// marker is retained on any failure so an exact ResourceRef can retry safely.
func removeRuntimeIO(ctx context.Context, dir string, ref ResourceRef) error {
	if dir == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name, data, err := runtimeIOIdentity(ref)
	if err != nil {
		return err
	}
	root, err := openRuntimeIORoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	marker, err := openRuntimeIOMarker(root, name, data, false)
	if os.IsNotExist(err) {
		if _, dirErr := root.Lstat(name); os.IsNotExist(dirErr) {
			return syncRuntimeIODir(root, ".")
		}
		return fmt.Errorf("refuse to remove FIFO directory without owner marker")
	}
	if err != nil {
		return err
	}
	defer marker.Close()
	nodes, _, err := inspectRuntimeIO(ctx, root, name)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := root.Lstat(node.path)
		if err != nil || !os.SameFile(node.info, current) || node.info.Mode() != current.Mode() {
			return fmt.Errorf("FIFO node changed during cleanup")
		}
		if err = root.Remove(node.path); err != nil {
			return err
		}
	}
	if err = syncRuntimeIODir(root, "."); err != nil {
		return err
	}
	info, err := marker.Stat()
	if err != nil {
		return err
	}
	current, err := root.Lstat(name + ".owner")
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf("FIFO ownership marker changed during cleanup")
	}
	if err = root.Remove(name + ".owner"); err != nil {
		return err
	}
	return syncRuntimeIODir(root, ".")
}

// RuntimeIOIdentity 返回私有 IO 根的非秘密路径与文件系统身份摘要
func (s *Containerd) RuntimeIOIdentity() (string, error) {
	dir := s.config.FIFODir
	if dir == "" {
		return "", nil
	}
	if err := validateFIFODir(dir); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("FIFO root filesystem identity unavailable")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("v1\x00%s\x00%d\x00%d", dir, stat.Dev, stat.Ino)))
	return hex.EncodeToString(sum[:]), nil
}
