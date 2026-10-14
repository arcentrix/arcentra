// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFIFODirectoryRejectsUntrustedAncestors 验证私有叶目录不能掩盖可替换的父目录
func TestFIFODirectoryRejectsUntrustedAncestors(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	fifo := filepath.Join(parent, "fifo")
	if err := os.MkdirAll(fifo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0777, 0770, 0722} {
		if err := os.Chmod(parent, mode); err != nil {
			t.Fatal(err)
		}
		if err := validateFIFODir(fifo); err == nil {
			t.Fatalf("writable ancestor mode %04o was accepted", mode)
		}
		info, err := os.Stat(parent)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("validation mutated parent permissions: %v %v", info, err)
		}
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateFIFODir(fifo); err != nil {
		t.Fatalf("private trusted hierarchy rejected: %v", err)
	}
}

// TestFIFODirectoryRejectsAncestorSymlink 验证父级符号链接不能隐藏路径变更
func TestFIFODirectoryRejectsAncestorSymlink(t *testing.T) {
	root := t.TempDir()
	actual := filepath.Join(root, "actual")
	fifo := filepath.Join(actual, "fifo")
	if err := os.MkdirAll(fifo, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeConfig(&ContainerdConfig{FIFODir: filepath.Join(alias, "fifo")}); err == nil {
		t.Fatal("FIFO configuration followed an ancestor symlink")
	}
	entries, err := os.ReadDir(fifo)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected configuration changed target contents: %v %v", entries, err)
	}
}

// TestFIFODirectoryReplacementFailsRevalidation 验证启动校验后替换目录不能继续授权 IO
func TestFIFODirectoryReplacementFailsRevalidation(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "fifo")
	if err := os.Mkdir(fifo, 0700); err != nil {
		t.Fatal(err)
	}
	if cfg, err := normalizeConfig(&ContainerdConfig{FIFODir: fifo}); err != nil || cfg.FIFODir != fifo {
		t.Fatalf("explicit private FIFO root lost: %+v %v", cfg, err)
	}
	if err := os.Rename(fifo, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	if err := validateFIFODir(fifo); err == nil {
		t.Fatal("missing root was recreated or accepted")
	}
	if _, err := os.Lstat(fifo); !os.IsNotExist(err) {
		t.Fatalf("validation recreated missing directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "original"), fifo); err != nil {
		t.Fatal(err)
	}
	if err := validateFIFODir(fifo); err == nil {
		t.Fatal("replaced symlink root was accepted")
	}
	info, err := os.Lstat(fifo)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("validation replaced the caller-owned node: %v %v", info, err)
	}
}

func TestRuntimeIOIdentityRejectsRootReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "fifo")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	backend := newSandbox(ContainerdConfig{FIFODir: root}, nil)
	before, err := backend.RuntimeIOIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(parent, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	after, err := backend.RuntimeIOIdentity()
	if err != nil || before == after {
		t.Fatal("FIFO root inode replacement did not change runtime binding", err)
	}
}
