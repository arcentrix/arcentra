// Copyright 2026 Arcentra Authors.
// Licensed under the Apache License, Version 2.0.

package sandboxexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arcentrix/arcentra/pkg/sandbox"
	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

const journalVersion = 1
const maxJournalRecordBytes = 16 * 1024
const journalBindingFile = "owner.json"
const journalLockFile = ".lock"

const (
	phaseIdentityConflict = "identity-conflict"
	phaseIntent           = "intent"
	phaseAllocated        = "allocated"
	phaseCreating         = "creating"
	phaseCreated          = "created"
	phaseStarting         = "starting"
	phaseRunning          = "running"
	phaseExecuting        = "executing"
	phaseSealing          = "sealing"
	phaseSealed           = "sealed"
	phaseRemoving         = "removing"
	phaseClosed           = "closed"
	phaseRecovered        = "recovered"
)

type journalRecord struct {
	ID                   string              `json:"id"`
	PipelineRunID        string              `json:"pipelineRunID"`
	JobRunID             string              `json:"jobRunID"`
	ExecutionClaimID     string              `json:"executionClaimID"`
	RuntimeBindingDigest string              `json:"runtimeBindingDigest"`
	Directory            string              `json:"directory"`
	Device               uint64              `json:"device"`
	Inode                uint64              `json:"inode"`
	Phase                string              `json:"phase"`
	Resource             sandbox.ResourceRef `json:"resource"`
}

type journalDocument struct {
	Version       int            `json:"version"`
	ControllerID  string         `json:"controllerID"`
	AgentID       string         `json:"agentID"`
	WorkspaceRoot string         `json:"workspaceRoot"`
	Namespace     string         `json:"namespace"`
	Record        *journalRecord `json:"record,omitempty"`
	Checksum      string         `json:"checksum"`
}

func (c *Controller) document(record *journalRecord) journalDocument {
	return journalDocument{
		Version: journalVersion, ControllerID: c.opts.ControllerID, AgentID: c.opts.AgentID,
		WorkspaceRoot: c.opts.WorkspaceRoot, Namespace: c.backend.Namespace(), Record: record,
	}
}

func encodeJournal(document journalDocument) ([]byte, error) {
	document.Checksum = ""
	body, err := sonic.Marshal(document)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(body)
	document.Checksum = hex.EncodeToString(checksum[:])
	return sonic.Marshal(document)
}

func (c *Controller) decodeJournal(data []byte) (*journalRecord, error) {
	var document journalDocument
	if err := sonic.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("invalid sandbox journal encoding")
	}
	canonical, err := encodeJournal(document)
	if err != nil || !bytes.Equal(data, canonical) {
		return nil, fmt.Errorf("sandbox journal checksum or canonical encoding mismatch")
	}
	expected := c.document(nil)
	if document.Version != expected.Version ||
		document.ControllerID != expected.ControllerID ||
		document.AgentID != expected.AgentID ||
		document.WorkspaceRoot != expected.WorkspaceRoot ||
		document.Namespace != expected.Namespace {
		return nil, fmt.Errorf("sandbox journal belongs to a different controller, Agent, runtime, or workspace")
	}
	return document.Record, nil
}

func openJournalDirectory(ctx context.Context, path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || path != filepath.Clean(path) || path == string(os.PathSeparator) {
		return nil, fmt.Errorf("sandbox root must be a clean absolute directory")
	}
	root, err := os.OpenRoot(filepath.VolumeName(path) + string(os.PathSeparator))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(path, filepath.VolumeName(path)+string(os.PathSeparator)), string(os.PathSeparator))
	for i, part := range parts {
		if err := ctx.Err(); err != nil {
			_ = root.Close()
			return nil, err
		}
		info, err := root.Lstat(part)
		if err == nil {
			if i == len(parts)-1 {
				err = privateJournalNode(info, true)
			} else {
				err = trustedJournalAncestor(info)
			}
		}
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("inspect sandbox root: %w", err)
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		actual, actualErr := next.Stat(".")
		after, afterErr := root.Lstat(part)
		if actualErr != nil || afterErr != nil || !os.SameFile(info, actual) || !os.SameFile(info, after) {
			_ = next.Close()
			_ = root.Close()
			return nil, fmt.Errorf("sandbox root changed while opening")
		}
		if err := root.Close(); err != nil {
			_ = next.Close()
			return nil, err
		}
		root = next
	}
	return root, nil
}

func (c *Controller) verifyRoots(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, root := range []struct {
		path string
		root *os.Root
		info os.FileInfo
	}{{c.opts.JournalRoot, c.journal, c.journalInfo}, {c.opts.WorkspaceRoot, c.workspaceRoot, c.workspaceInfo}} {
		current, err := root.root.Stat(".")
		visible, visibleErr := os.Lstat(root.path)
		if err != nil ||
			visibleErr != nil ||
			privateJournalNode(current, true) != nil ||
			privateJournalNode(visible, true) != nil ||
			!os.SameFile(current, root.info) ||
			!os.SameFile(visible, root.info) {
			return fmt.Errorf("sandbox root ownership or identity changed")
		}
		canonical, err := filepath.EvalSymlinks(root.path)
		if err != nil || canonical != root.path {
			return fmt.Errorf("sandbox root path changed")
		}
	}
	if c.lock != nil {
		actual, err := c.lock.Stat()
		visible, visibleErr := c.journal.Lstat(journalLockFile)
		if err != nil ||
			visibleErr != nil ||
			privateJournalNode(actual, false) != nil ||
			privateJournalNode(visible, false) != nil ||
			!os.SameFile(actual, visible) ||
			actual.Size() != 0 {
			return fmt.Errorf("sandbox journal lock identity changed")
		}
	}
	return nil
}

func (c *Controller) openJournalFile(ctx context.Context, name string, create bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := c.journal.Lstat(name)
	flags := os.O_RDWR | journalOpenFlags()
	if errors.Is(err, os.ErrNotExist) && create {
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil {
		return nil, err
	} else if err = privateJournalNode(before, false); err != nil {
		return nil, err
	}
	file, err := c.journal.OpenFile(name, flags, 0o600)
	if err != nil {
		return nil, err
	}
	actual, statErr := file.Stat()
	visible, visibleErr := c.journal.Lstat(name)
	if statErr != nil ||
		visibleErr != nil ||
		privateJournalNode(actual, false) != nil ||
		privateJournalNode(visible, false) != nil ||
		!os.SameFile(actual, visible) ||
		before != nil && !os.SameFile(before, actual) {
		_ = file.Close()
		return nil, fmt.Errorf("sandbox journal file identity changed")
	}
	return file, nil
}

func (c *Controller) readJournal(ctx context.Context, name string) (*journalRecord, error) {
	file, err := c.openJournalFile(ctx, name, false)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > maxJournalRecordBytes {
		return nil, fmt.Errorf("sandbox journal record exceeds allowed size or is empty")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJournalRecordBytes+1))
	if err != nil || len(data) > maxJournalRecordBytes {
		return nil, fmt.Errorf("read sandbox journal failed")
	}
	return c.decodeJournal(data)
}

func (c *Controller) fault(ctx context.Context, point string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.hook != nil {
		return c.hook(point)
	}
	return nil
}

func (c *Controller) syncJournal(ctx context.Context) error {
	if err := c.fault(ctx, "directory.sync"); err != nil {
		return err
	}
	return c.journalDir.Sync()
}

func (c *Controller) writeJournal(ctx context.Context, name string, record *journalRecord) error {
	if err := c.verifyRoots(ctx); err != nil {
		return err
	}
	data, err := encodeJournal(c.document(record))
	if err != nil || len(data) > maxJournalRecordBytes {
		return fmt.Errorf("sandbox journal record exceeds allowed size")
	}
	stage := ".pending-" + uuid.NewString()
	file, err := c.journal.OpenFile(stage, os.O_RDWR|os.O_CREATE|os.O_EXCL|journalOpenFlags(), 0o600)
	if err != nil {
		return err
	}
	// 任何写入不确定性保留暂存文件，下一次启动拒绝猜测提交状态
	defer file.Close()
	if err := c.fault(ctx, "record.write"); err != nil {
		return err
	}
	if n, err := file.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err := c.fault(ctx, "record.sync"); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := c.fault(ctx, "record.rename"); err != nil {
		return err
	}
	if err := c.journal.Rename(stage, name); err != nil {
		return err
	}
	return c.syncJournal(ctx)
}

func (c *Controller) resourceOwner(record journalRecord) string {
	body, _ := sonic.Marshal([]string{
		c.opts.ControllerID, c.opts.AgentID, record.PipelineRunID, record.JobRunID, record.ExecutionClaimID,
		record.RuntimeBindingDigest, record.ID, c.opts.WorkspaceRoot, record.Directory,
		record.Resource.ID, record.Resource.Namespace, record.Resource.Snapshotter,
	})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func (c *Controller) validateRecord(record *journalRecord, name string) error {
	if record == nil {
		return fmt.Errorf("sandbox journal record is absent")
	}
	id, err := uuid.Parse(record.ID)
	if err != nil ||
		id.String() != record.ID ||
		name != record.ID+".json" ||
		record.Directory != "job-"+record.ID ||
		record.Resource.ID != "arcentra-"+record.ID ||
		record.Resource.Namespace != c.backend.Namespace() ||
		!jobIDPattern.MatchString(record.JobRunID) ||
		!jobIDPattern.MatchString(record.ExecutionClaimID) ||
		!validRuntimeBinding(record.RuntimeBindingDigest) ||
		record.PipelineRunID != "" && !jobIDPattern.MatchString(record.PipelineRunID) ||
		record.Resource.Snapshotter == "" ||
		len(record.Resource.Snapshotter) > 128 ||
		strings.ContainsAny(record.Resource.Snapshotter, "\x00\r\n/\\") ||
		record.Resource.Owner != c.resourceOwner(*record) {
		return fmt.Errorf("sandbox journal resource identity is invalid")
	}
	switch record.Phase {
	case phaseIntent:
		if record.Inode != 0 || record.Device != 0 {
			return fmt.Errorf("sandbox intent has ambiguous workspace identity")
		}
	case phaseClosed, phaseRecovered:
		if record.Inode == 0 && record.Device != 0 {
			return fmt.Errorf("sandbox terminal workspace identity is invalid")
		}
	case phaseIdentityConflict, phaseAllocated, phaseCreating, phaseCreated, phaseStarting, phaseRunning, phaseExecuting, phaseSealing, phaseSealed, phaseRemoving:
		if record.Inode == 0 {
			return fmt.Errorf("sandbox journal workspace identity is absent")
		}
	default:
		return fmt.Errorf("sandbox journal phase is invalid")
	}
	return nil
}

func (c *Controller) verifyRecordWorkspace(record journalRecord, allowMissing bool) (os.FileInfo, error) {
	info, err := c.workspaceRoot.Lstat(record.Directory)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect recorded workspace: %w", err)
	}
	dev, ino, err := workspaceFileIdentity(info)
	if err != nil || record.Inode == 0 || dev != record.Device || ino != record.Inode {
		return nil, fmt.Errorf("recorded workspace ownership or identity changed")
	}
	return info, nil
}

func validRuntimeBinding(value string) bool {
	if !strings.HasPrefix(value, "v1:") {
		return false
	}
	digest := strings.TrimPrefix(value, "v1:")
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == digest
}
