package identitystore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func openPrivateDirectory(ctx context.Context, path string, hook faultHook) (_ *os.Root, retErr error) {
	volume := filepath.VolumeName(path) + string(os.PathSeparator)
	root, err := os.OpenRoot(volume)
	if err != nil {
		return nil, errIO
	}
	defer func() {
		if retErr != nil {
			_ = root.Close()
		}
	}()
	info, err := root.Stat(".")
	if err != nil || validateTrustedAncestor(info) != nil {
		return nil, errUnsafe
	}
	parts := strings.Split(strings.TrimPrefix(path, volume), string(os.PathSeparator))
	for index, component := range parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		final := index == len(parts)-1
		info, err := root.Lstat(component)
		if errors.Is(err, os.ErrNotExist) && final {
			if err := invoke(ctx, hook, "directory.before_create"); err != nil {
				return nil, err
			}
			if err := root.Mkdir(component, 0o700); err != nil {
				return nil, errIO
			}
			if err := invoke(ctx, hook, "directory.after_create"); err != nil {
				return nil, err
			}
			parent, openErr := root.Open(".")
			if openErr != nil {
				return nil, errIO
			}
			err = invoke(ctx, hook, "directory.before_parent_sync")
			if err == nil && parent.Sync() != nil {
				err = errIO
			}
			if err == nil {
				err = invoke(ctx, hook, "directory.after_parent_sync")
			}
			closeErr := parent.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, errIO
			}
			info, err = root.Lstat(component)
		}
		if err != nil {
			return nil, errIO
		}
		validate := validateTrustedAncestor
		if final {
			validate = validatePrivateDirectory
		}
		if err := validate(info); err != nil {
			return nil, err
		}
		next, err := root.OpenRoot(component)
		if err != nil {
			return nil, errIO
		}
		actual, actualErr := next.Stat(".")
		after, afterErr := root.Lstat(component)
		if actualErr != nil || afterErr != nil || validate(actual) != nil || validate(after) != nil || !os.SameFile(info, actual) || !os.SameFile(info, after) {
			_ = next.Close()
			return nil, errChanged
		}
		if err := root.Close(); err != nil {
			_ = next.Close()
			return nil, errIO
		}
		root = next
	}
	return root, nil
}

func invoke(ctx context.Context, hook faultHook, point string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if hook != nil && hook(point) != nil {
		return errIO
	}
	return ctx.Err()
}

func (s *Store) inspectEntries(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dirInfo, err := s.root.Stat(".")
	if err != nil || validatePrivateDirectory(dirInfo) != nil || !os.SameFile(dirInfo, s.directoryInfo) {
		return errChanged
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return errIO
	}
	defer dir.Close()
	// At most four recognized files are valid. Read a bounded number to avoid
	// allocating unbounded memory for a hostile or accidentally reused directory.
	entries, err := dir.ReadDir(5)
	if err != nil && !errors.Is(err, io.EOF) {
		return errIO
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch entry.Name() {
		case recordName, stageName, guardName, lockName:
		default:
			return errors.New("agent identity directory contains unexpected entries")
		}
		info, err := s.root.Lstat(entry.Name())
		if err != nil {
			return errIO
		}
		if err := validatePrivateFile(info); err != nil {
			return err
		}
		if entry.Name() == lockName && info.Size() != 0 {
			return errInvalid
		}
	}
	return nil
}

func (s *Store) openPrivateFile(ctx context.Context, name string, allowCreate bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := s.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) && allowCreate {
		file, err := s.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|privateOpenFlags(), 0o600)
		if err != nil {
			return nil, errIO
		}
		if err := s.validateFile(name, file); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}
	if err != nil {
		return nil, errIO
	}
	if err := validatePrivateFile(info); err != nil {
		return nil, err
	}
	mode := os.O_RDONLY
	if allowCreate {
		mode = os.O_RDWR
	}
	file, err := s.root.OpenFile(name, mode|privateOpenFlags(), 0)
	if err != nil {
		return nil, errIO
	}
	if err := s.validateFile(name, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		_ = file.Close()
		return nil, errChanged
	}
	return file, nil
}

func (s *Store) validateFile(name string, file *os.File) error {
	actual, err := file.Stat()
	if err != nil {
		return errIO
	}
	current, err := s.root.Lstat(name)
	if err != nil {
		return errIO
	}
	if err := validatePrivateFile(actual); err != nil {
		return err
	}
	if err := validatePrivateFile(current); err != nil {
		return err
	}
	if !os.SameFile(actual, current) {
		return errChanged
	}
	return nil
}

func (s *Store) verifySnapshot(ctx context.Context) error {
	if err := s.inspectEntries(ctx); err != nil {
		return err
	}
	if err := s.validateFile(lockName, s.lock); err != nil {
		return err
	}
	if _, err := s.root.Lstat(stageName); err == nil {
		return ErrPending
	} else if !errors.Is(err, os.ErrNotExist) {
		return errIO
	}
	rec, exists, err := s.readRecord(ctx)
	if err != nil {
		return err
	}
	_, guardErr := s.root.Lstat(guardName)
	switch s.state {
	case StateEmpty:
		if exists || !errors.Is(guardErr, os.ErrNotExist) {
			return errChanged
		}
	case StatePending:
		if err := s.validateGuard(); err != nil {
			return err
		}
		if !exists || guardErr != nil || rec.State != StatePending || rec.ControllerBinding != s.binding {
			return errChanged
		}
	case StateReady:
		if !exists || !errors.Is(guardErr, os.ErrNotExist) || rec.State != StateReady || rec.ControllerBinding != s.binding || rec.AgentID != s.identity.AgentID || rec.PerAgentToken != s.identity.PerAgentToken {
			return errChanged
		}
	default:
		return errChanged
	}
	return nil
}

func (s *Store) validateGuard() error {
	current, err := s.root.Lstat(guardName)
	if err != nil || validatePrivateFile(current) != nil || s.guardInfo == nil || !os.SameFile(s.guardInfo, current) {
		return errChanged
	}
	return nil
}
